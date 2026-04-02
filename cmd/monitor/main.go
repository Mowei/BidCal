package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

type AuctionData struct {
	Stat   string     `json:"stat"`
	Date   int        `json:"date"`
	Title  string     `json:"title"`
	Fields []string   `json:"fields"`
	Data   [][]string `json:"data"`
}

type YuantaStockPrice struct {
	Data struct {
		Deal           float64 `json:"deal"`
		FlatPlatePrice float64 `json:"flatPlatePrice"`
		Name           string  `json:"name"`
	} `json:"data"`
	Status int `json:"status"`
}

type MonitorConfig struct {
	TargetROI          float64
	MinProfit          float64
	CommissionDiscount float64 // 手續費折扣率 (0.6 = 60%)
	EmailTo            string
	EmailFrom          string
	EmailPass          string
	SMTPServer         string
	SMTPPort           string
}

type AuctionResult struct {
	StockCode        string
	StockName        string
	CurrentPrice     float64
	MinBidPrice      float64
	Quantity         float64 // 投標數量（張）
	RecommendedPrice float64
	SellCommission   float64 // 賣出手續費
	TransactionTax   float64 // 證券交易稅
	BidProcessingFee float64 // 投標處理費
	AwardCommission  float64 // 得標手續費
	NetProfit        float64 // 淨利潤
	Recommendation   string
	RejectReason     string // 非空表示不符合條件
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

func main() {
	logFile, err := os.OpenFile(
		fmt.Sprintf("monitor-%s.log", time.Now().Format("2006-01-02-15-04-05")),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0666,
	)
	if err != nil {
		log.Fatalf("無法建立日誌文件: %v", err)
	}
	defer logFile.Close()

	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	log.Println("========================")
	log.Println("競拍監控系統 - 開始執行")
	log.Println("========================")

	// 讀取配置
	config := loadConfig()
	log.Printf("配置 - 目標報酬率: %.0f%%, 最小損益: ¥%.0f\n", config.TargetROI, config.MinProfit)

	// 取得拍賣清單
	auctions := fetchAuctions()
	if len(auctions) == 0 {
		log.Println("❌ 無法取得拍賣清單")
		return
	}
	log.Printf("✓ 取得 %d 筆拍賣標案\n", len(auctions))

	// 分析每個標案
	results := []AuctionResult{}
	for _, auction := range auctions {
		stockName := auction["證券名稱"]
		stockCode := auction["證券代號"]
		market := auction["發行市場"]

		result := analyzeAuction(auction, config)
		if result == nil {
			log.Printf("✗ %s (%s) [%s]: 資料無效\n", stockName, stockCode, market)
			continue
		}

		if result.RejectReason == "" {
			results = append(results, *result)
			log.Printf("✓ %s (%s) [%s]: 投標價 ¥%.2f，淨利 ¥%.0f\n",
				result.StockName, result.StockCode, market, result.RecommendedPrice, result.NetProfit)
		} else {
			log.Printf("✗ %s (%s) [%s]: 不符合條件\n", stockName, stockCode, market)
			log.Printf("  └─ 不符合條件（%s）\n", result.RejectReason)
		}

		if result.CurrentPrice > 0 {
			log.Printf("  └─ 現價          : ¥%.2f\n", result.CurrentPrice)
		}
		if result.RecommendedPrice > 0 {
			log.Printf("  └─ 反推投標價    : ¥%.2f\n", result.RecommendedPrice)
			log.Printf("  └─ 賣出手續費    : ¥%.0f\n", result.SellCommission)
			log.Printf("  └─ 證券交易稅    : ¥%.0f\n", result.TransactionTax)
			log.Printf("  └─ 得標手續費    : ¥%.0f\n", result.AwardCommission)
			log.Printf("  └─ 投標處理費    : ¥%.0f\n", result.BidProcessingFee)
			log.Printf("  └─ 淨利潤        : ¥%.0f\n", result.NetProfit)

		}
	}

	// 發送通知
	if len(results) > 0 {
		log.Printf("\n📧 發送通知：%d 個符合條件的機會", len(results))
		sendEmailNotification(results, config)
		log.Println("✓ 通知已發送")
	} else {
		log.Println("✗ 無符合目標報酬的機會")
	}

	log.Println("========================")
	log.Println("執行完成")
}

func loadConfig() MonitorConfig {
	config := MonitorConfig{
		TargetROI:          parseFloat(os.Getenv("TARGET_ROI"), 30),
		MinProfit:          parseFloat(os.Getenv("MIN_PROFIT"), 20000),
		CommissionDiscount: parseFloat(os.Getenv("COMMISSION_DISCOUNT"), 0.6),
		EmailTo:            os.Getenv("EMAIL_TO"),
		EmailFrom:          os.Getenv("EMAIL_FROM"),
		EmailPass:          os.Getenv("EMAIL_PASSWORD"),
		SMTPServer:         os.Getenv("SMTP_SERVER"),
		SMTPPort:           os.Getenv("SMTP_PORT"),
	}

	if config.SMTPServer == "" {
		config.SMTPServer = "smtp.gmail.com"
	}
	if config.SMTPPort == "" {
		config.SMTPPort = "587"
	}

	return config
}

func parseFloat(s string, defaultVal float64) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

func fetchAuctions() []map[string]string {
	resp, err := httpClient.Get("https://www.twse.com.tw/rwd/zh/announcement/auction?response=json")
	if err != nil {
		log.Printf("❌ 拍賣清單請求失敗: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var auctionData AuctionData
	if err := json.Unmarshal(body, &auctionData); err != nil {
		log.Printf("❌ JSON 解析失敗: %v\n", err)
		return nil
	}

	if auctionData.Stat != "OK" {
		log.Printf("❌ API 回應異常: %s\n", auctionData.Stat)
		return nil
	}

	results := []map[string]string{}
	for _, row := range auctionData.Data {
		if len(row) < 25 {
			continue
		}

		// 檢查是否進行中（投標結束日 >= 今天）
		endDate := row[8]
		if endDate < time.Now().Format("2006/01/02") {
			continue
		}

		auction := make(map[string]string)
		for i, field := range auctionData.Fields {
			if i < len(row) {
				auction[field] = row[i]
			}
		}
		results = append(results, auction)
	}

	return results
}

func analyzeAuction(auction map[string]string, config MonitorConfig) *AuctionResult {
	stockCode := auction["證券代號"]
	stockName := auction["證券名稱"]
	minBidStr := strings.TrimSpace(auction["最低投標價格(元)"])
	minQuantityStr := strings.TrimSpace(auction["最低每標單投標數量(張)"])
	processingFeeStr := strings.TrimSpace(auction["每一投標單投標處理費(元)"])
	awardCommRateStr := strings.TrimSpace(auction["得標手續費率(%)"])

	minBid := parseFloat(minBidStr, 0)
	totalQuantity := parseFloat(minQuantityStr, 0)
	bidProcessingFee := parseFloat(strings.ReplaceAll(processingFeeStr, ",", ""), 0)
	awardCommRate := parseFloat(awardCommRateStr, 0)
	if minBid <= 0 {
		return &AuctionResult{StockCode: stockCode, StockName: stockName, RejectReason: "最低投標價格無效"}
	}
	if totalQuantity <= 0 {
		return &AuctionResult{StockCode: stockCode, StockName: stockName, RejectReason: "最低每標單投標數量無效"}
	}

	// 取得現價
	currentPrice := fetchStockPrice(stockCode)
	if currentPrice <= 0 {
		log.Printf("⚠ 無法取得 %s (%s) 的現價\n", stockName, stockCode)
		return &AuctionResult{StockCode: stockCode, StockName: stockName, RejectReason: "無法取得現價"}
	}

	const sharesPerLot = 1000.0
	qty := totalQuantity * sharesPerLot
	targetRoi := config.TargetROI / 100
	winRate := awardCommRate / 100

	sellCommRate := 0.001425 * config.CommissionDiscount
	transactionTaxRate := 0.003
	netReceivePerShare := currentPrice * (1 - sellCommRate - transactionTaxRate)

	recommendedPrice := (netReceivePerShare/(1+targetRoi) - bidProcessingFee/qty) / (1 + winRate)

	partial := &AuctionResult{
		StockCode:        stockCode,
		StockName:        stockName,
		CurrentPrice:     currentPrice,
		MinBidPrice:      minBid,
		Quantity:         totalQuantity,
		RecommendedPrice: recommendedPrice,
	}

	if recommendedPrice <= 0 {
		partial.RejectReason = "反推投標價為負或零"
		return partial
	}
	if recommendedPrice < minBid {
		partial.RejectReason = fmt.Sprintf("反推投標價低於最低投標價（最低 ¥%.2f）", minBid)
		return partial
	}
	if recommendedPrice > currentPrice {
		partial.RejectReason = "反推投標價高於現價"
		return partial
	}

	// 計算各項費用與淨利
	saleAmount := currentPrice * qty
	bidAmount := recommendedPrice * qty
	expectedProfit := saleAmount - bidAmount
	sellCommission := math.Max(saleAmount*0.001425*config.CommissionDiscount, 20)
	transactionTax := saleAmount * transactionTaxRate
	awardCommission := bidAmount * winRate
	netProfit := expectedProfit - sellCommission - transactionTax - awardCommission - bidProcessingFee

	partial.SellCommission = sellCommission
	partial.TransactionTax = transactionTax
	partial.BidProcessingFee = bidProcessingFee
	partial.AwardCommission = awardCommission
	partial.NetProfit = netProfit

	if netProfit < config.MinProfit {
		partial.RejectReason = fmt.Sprintf("淨利未達門檻（%.0f < %.0f）", netProfit, config.MinProfit)
		return partial
	}

	partial.Recommendation = fmt.Sprintf("投標價 ¥%.2f，毛利 ¥%.0f，賣出手續費 ¥%.0f，證交稅 ¥%.0f，得標手續費 ¥%.0f，投標處理費 ¥%.0f，淨利 ¥%.0f（%.0f 張）",
		recommendedPrice, expectedProfit, sellCommission, transactionTax, awardCommission, bidProcessingFee, netProfit, totalQuantity)
	return partial
}

func fetchStockPrice(stockCode string) float64 {
	// 使用 Yuanta API 查詢股價（支援上市、上櫃、興櫃）
	url := fmt.Sprintf("https://ytdf.yuanta.com.tw/prod/yesidmz/api/basic/currentstock?symbol=%s", stockCode)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("  └─ API 請求失敗: %v\n", err)
		return -1
	}

	// 設置 User-Agent 和 Referer，模擬瀏覽器請求
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.yuanta.com.tw/")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("  └─ 網路請求失敗: %v\n", err)
		return -1
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var yuantaData YuantaStockPrice
	if err := json.Unmarshal(body, &yuantaData); err == nil {
		price := yuantaData.Data.Deal
		if price <= 0 {
			price = yuantaData.Data.FlatPlatePrice
		}
		if price > 0 {
			return price
		}
	}

	log.Printf("  └─ 無法解析股價數據\n")
	return -1
}

func sendEmailNotification(results []AuctionResult, config MonitorConfig) error {
	if config.EmailTo == "" || config.EmailFrom == "" || config.EmailPass == "" {
		log.Println("⚠ Email 配置不完整，跳過發送")
		return nil
	}

	// 建立郵件內容
	subject := fmt.Sprintf("競拍監控提醒 - 發現 %d 個符合條件的機會", len(results))
	body := buildEmailBody(results)

	// 發送郵件
	auth := smtp.PlainAuth("", config.EmailFrom, config.EmailPass, config.SMTPServer)
	addr := fmt.Sprintf("%s:%s", config.SMTPServer, config.SMTPPort)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		config.EmailFrom, config.EmailTo, subject, body)

	err := smtp.SendMail(addr, auth, config.EmailFrom, []string{config.EmailTo}, []byte(msg))
	if err != nil {
		log.Printf("❌ Email 發送失敗: %v\n", err)
		return err
	}

	return nil
}

func buildEmailBody(results []AuctionResult) string {
	var buf bytes.Buffer

	buf.WriteString(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: Arial, sans-serif; background: #f5f5f5; }
        .container { max-width: 600px; margin: 20px auto; background: white; padding: 20px; border-radius: 8px; }
        .header { background: #2c3e50; color: white; padding: 15px; border-radius: 4px; margin-bottom: 20px; }
        .opportunity { border: 1px solid #ecf0f1; padding: 15px; margin-bottom: 15px; border-radius: 4px; }
        .stock-code { font-weight: bold; color: #e74c3c; }
        .price-info { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin: 10px 0; }
        .price-item { background: #ecf0f1; padding: 10px; border-radius: 3px; }
        .label { color: #7f8c8d; font-size: 12px; }
        .value { font-weight: bold; font-size: 14px; }
        .footer { color: #7f8c8d; font-size: 12px; margin-top: 20px; border-top: 1px solid #ecf0f1; padding-top: 10px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h2>📊 競拍投資機會提醒</h2>
        </div>
`)

	for _, result := range results {
		buf.WriteString(fmt.Sprintf(`
        <div class="opportunity">
            <div><span class="stock-code">%s</span> - %s</div>
            <div class="price-info">
                <div class="price-item">
                    <div class="label">現價</div>
                    <div class="value">¥%.2f</div>
                </div>
                <div class="price-item">
                    <div class="label">推薦投標價</div>
                    <div class="value">¥%.2f</div>
                </div>
                <div class="price-item">
                    <div class="label">投標數量</div>
                    <div class="value">%.0f 張</div>
                </div>
                <div class="price-item">
                    <div class="label">毛利潤</div>
                    <div class="value" style="color: #27ae60;">¥%.0f</div>
                </div>
                <div class="price-item">
                    <div class="label">賣出手續費</div>
                    <div class="value" style="color: #e74c3c;">¥%.0f</div>
                </div>
				<div class="price-item">
					<div class="label">證券交易稅</div>
					<div class="value" style="color: #e74c3c;">¥%.0f</div>
				</div>
                <div class="price-item">
                    <div class="label">得標手續費</div>
                    <div class="value" style="color: #e74c3c;">¥%.0f</div>
                </div>
                <div class="price-item">
                    <div class="label">投標處理費</div>
                    <div class="value" style="color: #e74c3c;">¥%.0f</div>
                </div>
                <div class="price-item">
                    <div class="label">淨利潤</div>
                    <div class="value" style="color: #2980b9; font-weight: bold; font-size: 16px;">¥%.0f</div>
                </div>
            </div>
        </div>
`, result.StockCode, result.StockName, result.CurrentPrice, result.RecommendedPrice, result.Quantity, (result.CurrentPrice-result.RecommendedPrice)*result.Quantity*1000, result.SellCommission, result.TransactionTax, result.AwardCommission, result.BidProcessingFee, result.NetProfit))
	}

	buf.WriteString(`
        <div class="footer">
            <p>📌 此通知由自動監控系統產生 | `)
	buf.WriteString(time.Now().Format("2006/01/02 15:04:05"))
	buf.WriteString(`</p>
        </div>
    </div>
</body>
</html>
`)

	return buf.String()
}
