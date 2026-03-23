package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

// proxy 向 targetURL 發起 GET 並將結果回寫給瀏覽器
func proxy(w http.ResponseWriter, targetURL string) {
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		http.Error(w, `{"error":"proxy build error"}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.twse.com.tw/")

	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, `{"error":"upstream unreachable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func main() {
	mux := http.NewServeMux()

	// 代理：競拍標案清單
	mux.HandleFunc("/proxy/auction", func(w http.ResponseWriter, r *http.Request) {
		proxy(w, "https://www.twse.com.tw/rwd/zh/announcement/auction?response=json")
	})

	// 代理：即時股價（只允許 ex_ch 參數，防止 SSRF）
	mux.HandleFunc("/proxy/stockprice", func(w http.ResponseWriter, r *http.Request) {
		exCh := r.URL.Query().Get("ex_ch")
		if exCh == "" {
			http.Error(w, `{"msgArray":[]}`, http.StatusBadRequest)
			return
		}
		params := url.Values{
			"ex_ch": {exCh},
			"json":  {"1"},
			"delay": {"0"},
		}
		proxy(w, "https://mis.twse.com.tw/stock/api/getStockInfo.jsp?"+params.Encode())
	})

	// 代理：個股日成交資訊（取收盤價）
	mux.HandleFunc("/proxy/stockday", func(w http.ResponseWriter, r *http.Request) {

		stockNo := r.URL.Query().Get("stockNo")
		if stockNo == "" {
			http.Error(w, `{"stat":"error"}`, http.StatusBadRequest)
			return
		}
		params := url.Values{
			"symbol": {stockNo},
		}
		proxy(w, "https://ytdf.yuanta.com.tw/prod/yesidmz/api/basic/currentstock?"+params.Encode())
	})

	// 靜態檔案（index.html、其他資源）
	mux.Handle("/", http.FileServer(http.Dir(".")))

	addr := "localhost:8080"
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║   證交所競拍計算機  本地伺服器       ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Printf("  網址：http://%s/index.html\n", addr)
	fmt.Println("  按 Ctrl+C 停止")
	fmt.Println()

	openBrowser("http://" + addr + "/index.html")
	log.Fatal(http.ListenAndServe(addr, mux))
}

func openBrowser(u string) {
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("cmd", "/c", "start", u).Start()
	case "darwin":
		err = exec.Command("open", u).Start()
	default:
		err = exec.Command("xdg-open", u).Start()
	}
	if err != nil {
		log.Printf("無法自動開啟瀏覽器：%v（請手動前往 http://localhost:8080/index.html）", err)
	}
}
