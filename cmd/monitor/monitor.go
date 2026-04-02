package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
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
	DiscordWebhook     string
}

type AuctionResult struct {
	StockCode        string
	StockName        string
	IssueType        string // 發行性質
	LeadBroker       string // 主辦券商
	EndDate          string // 投標結束日 2006/01/02
	IsReminder       bool   // 是否為截止提醒
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

const notifiedFile = "notified.json"

type notifiedEntry struct {
	EndDate      string `json:"end_date"`      // 投標結束日 2006/01/02
	ReminderSent bool   `json:"reminder_sent"` // 截止前一天提醒是否已送出
}

// loadNotified 讀取已通知清單
func loadNotified() map[string]notifiedEntry {
	data, err := os.ReadFile(notifiedFile)
	if err != nil {
		return map[string]notifiedEntry{}
	}
	var entries map[string]notifiedEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return map[string]notifiedEntry{}
	}
	return entries
}

// saveNotified 將已通知清單寫回 notified.json
func saveNotified(notified map[string]notifiedEntry) {
	data, _ := json.MarshalIndent(notified, "", "  ")
	_ = os.WriteFile(notifiedFile, data, 0644)
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

	// 載入已通知清單
	notified := loadNotified()
	log.Printf("已通知標的數: %d\n", len(notified))

	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006/01/02")

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
			entry, alreadyNotified := notified[result.StockCode]
			if alreadyNotified {
				// 結束前一天且尚未發過截止提醒
				if result.EndDate == tomorrow && !entry.ReminderSent {
					result.IsReminder = true
					log.Printf("⏰ %s (%s) [%s]: 截止提醒\n", result.StockName, result.StockCode, market)
				} else {
					log.Printf("⏭ %s (%s) [%s]: 已通知過，跳過\n", result.StockName, result.StockCode, market)
					continue
				}
			}
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
		log.Printf("\n📢 發送通知：%d 個符合條件的機會", len(results))
		sendDiscordNotification(results, config)
		for _, r := range results {
			entry := notified[r.StockCode]
			entry.EndDate = r.EndDate
			if r.IsReminder {
				entry.ReminderSent = true
			}
			notified[r.StockCode] = entry
		}
		saveNotified(notified)
		log.Println("✓ 通知已發送，已更新 notified.json")
	} else {
		log.Println("✗ 無符合目標報酬的機會")
	}

	log.Println("========================")
	log.Println("執行完成")
}

func loadConfig() MonitorConfig {
	return MonitorConfig{
		TargetROI:          parseFloat(os.Getenv("TARGET_ROI"), 30),
		MinProfit:          parseFloat(os.Getenv("MIN_PROFIT"), 20000),
		CommissionDiscount: parseFloat(os.Getenv("COMMISSION_DISCOUNT"), 0.6),
		DiscordWebhook:     os.Getenv("DISCORD_WEBHOOK"),
	}
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
	issueType := auction["發行性質"]
	leadBroker := auction["主辦券商"]
	endDate := auction["投標結束日"]
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
		IssueType:        issueType,
		LeadBroker:       leadBroker,
		EndDate:          endDate,
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

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordEmbed struct {
	Title  string         `json:"title"`
	Color  int            `json:"color"`
	Fields []discordField `json:"fields"`
	Footer struct {
		Text string `json:"text"`
	} `json:"footer"`
}

type discordPayload struct {
	Content string         `json:"content"`
	Embeds  []discordEmbed `json:"embeds"`
}

func sendDiscordNotification(results []AuctionResult, config MonitorConfig) error {
	if config.DiscordWebhook == "" {
		log.Println("⚠ DISCORD_WEBHOOK 未設定，跳過發送")
		return nil
	}

	// Discord 每次最多 10 個 embeds，分批發送
	const batchSize = 10
	for i := 0; i < len(results); i += batchSize {
		end := i + batchSize
		if end > len(results) {
			end = len(results)
		}
		batch := results[i:end]

		embeds := make([]discordEmbed, 0, len(batch))
		for _, r := range batch {
			grossProfit := (r.CurrentPrice - r.RecommendedPrice) * r.Quantity * 1000
			embed := discordEmbed{
				Title: fmt.Sprintf("%s（%s）", r.StockName, r.StockCode),
				Color: 3066993, // 綠色
				Fields: []discordField{
					{Name: "發行性質", Value: r.IssueType, Inline: true},
					{Name: "主辦券商", Value: r.LeadBroker, Inline: true},
					{Name: "現價", Value: fmt.Sprintf("¥%.2f", r.CurrentPrice), Inline: true},
					{Name: "推薦投標價", Value: fmt.Sprintf("¥%.2f", r.RecommendedPrice), Inline: true},
					{Name: "投標數量", Value: fmt.Sprintf("%.0f 張", r.Quantity), Inline: true},
					{Name: "毛利潤", Value: fmt.Sprintf("¥%.0f", grossProfit), Inline: true},
					{Name: "賣出手續費", Value: fmt.Sprintf("¥%.0f", r.SellCommission), Inline: true},
					{Name: "證券交易稅", Value: fmt.Sprintf("¥%.0f", r.TransactionTax), Inline: true},
					{Name: "得標手續費", Value: fmt.Sprintf("¥%.0f", r.AwardCommission), Inline: true},
					{Name: "投標處理費", Value: fmt.Sprintf("¥%.0f", r.BidProcessingFee), Inline: true},
					{Name: "淨利潤", Value: fmt.Sprintf("**¥%.0f**", r.NetProfit), Inline: true},
				},
			}
			embed.Footer.Text = time.Now().Format("2006/01/02 15:04:05")
			embeds = append(embeds, embed)
		}

		content := ""
		if i == 0 {
			reminderCount := 0
			newCount := 0
			for _, r := range results {
				if r.IsReminder {
					reminderCount++
				} else {
					newCount++
				}
			}
			parts := []string{}
			if newCount > 0 {
				parts = append(parts, fmt.Sprintf("**%d** 個新機會", newCount))
			}
			if reminderCount > 0 {
				parts = append(parts, fmt.Sprintf("**%d** 個明日截止提醒", reminderCount))
			}
			content = "📊 **競拍投資機會提醒** — " + strings.Join(parts, "、")
		}

		payload := discordPayload{Content: content, Embeds: embeds}
		body, err := json.Marshal(payload)
		if err != nil {
			log.Printf("❌ Discord payload 序列化失敗: %v\n", err)
			return err
		}

		resp, err := httpClient.Post(config.DiscordWebhook, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("❌ Discord 通知發送失敗: %v\n", err)
			return err
		}
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("❌ Discord 回應異常: HTTP %d\n", resp.StatusCode)
			return fmt.Errorf("discord webhook returned status %d", resp.StatusCode)
		}
	}

	return nil
}
