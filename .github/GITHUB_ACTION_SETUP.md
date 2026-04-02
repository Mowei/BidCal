# 競拍計算機 GitHub Action 設置指南

## 功能概述

自動監控台灣證交所競拍標案，根據以下條件發送 Email 通知：
- **目標報酬率**：30%
- **最小淨利潤**：¥20,000 以上（已扣除賣出手續費）
- **監控範圍**：進行中所有拍賣標案（上市、上櫃、興櫃）
- **執行頻率**：每天上午 9 點（台灣時間）

## 快速設置

### 1️⃣ 配置 GitHub Secrets

進入你的 GitHub Repository 設置：
`Settings → Secrets and variables → Actions → New repository secret`

需要設置以下 Secrets：

| Secret 名稱 | 說明 | 範例 |
|-----------|------|------|
| `EMAIL_TO` | 接收通知的 Email 地址 | `your.email@gmail.com` |
| `EMAIL_FROM` | 寄件 Email（建議用 Gmail App Password） | `your.email@gmail.com` |
| `EMAIL_PASSWORD` | Email 密碼或應用程式密碼 | `xxxx xxxx xxxx xxxx` |
| `SMTP_SERVER` | SMTP 伺服器（可選，預設 Gmail） | `smtp.gmail.com` |
| `SMTP_PORT` | SMTP Port（可選，預設 587） | `587` |

### 2️⃣ Gmail 應用程式密碼設置（推薦）

1. 啟用 Gmail 的 2 步驟驗證
2. 進入 [Google App Passwords](https://myaccount.google.com/apppasswords)
3. 選擇應用程式：Mail，選擇裝置：Other
4. 生成 16 碼密碼
5. 將密碼設置為 `EMAIL_PASSWORD` Secret

### 3️⃣ 其他 Email 服務配置

**Outlook/Hotmail:**
```
SMTP_SERVER: smtp-mail.outlook.com
SMTP_PORT: 587
```

**Yahoo Mail:**
```
SMTP_SERVER: smtp.mail.yahoo.com
SMTP_PORT: 587
```

### 4️⃣ 修改監控參數（可選）

編輯 `.github/workflows/auction-monitor.yml`：

```yaml
env:
  TARGET_ROI: '30'              # 目標報酬率：改為你想要的百分比
  MIN_PROFIT: '20000'           # 最小淨利潤：改為你想要的金額（已扣除手續費）
  COMMISSION_DISCOUNT: '0.6'    # 手續費折扣率：改為你的證券商折扣（預設 60%）
```

## 投標邏輯說明

### 反推投標價公式

基於目標報酬率反推合理投標價（使用競拍總數量）：

```
目標獲利 = 現價 × (目標報酬率) + 最小損益
推薦投標價 = 現價 - (目標獲利 ÷ 競拍數量)
```

### 手續費計算

賣出時需支付手續費（標準費率 0.1425% × 折扣率）：

```
賣出金額 = 現價 × 競拍數量
賣出手續費 = MAX(賣出金額 × 0.1425% × 折扣率, 20元)
毛利潤 = (現價 - 投標價) × 數量
淨利潤 = 毛利潤 - 賣出手續費  ← 需要 ≥ ¥20,000
```

**例子：**
- 股票現價：¥100
- 目標報酬率：30%
- 最小損益：¥20,000
- 競拍總數量：1,000 張
- 手續費折扣率：0.6 (60%)

計算：
```
目標獲利 = 100 × 30% + 20,000 = ¥20,030
推薦投標價 = 100 - (20,030 ÷ 1,000) = ¥79.97
毛利潤 = (100 - 79.97) × 1,000 = ¥20,030
賣出金額 = 100 × 1,000 = ¥100,000
賣出手續費 = MAX(100,000 × 0.1425% × 0.6, 20) = MAX(¥85.5, 20) = ¥85.5
淨利潤 = ¥20,030 - ¥85.5 = ¥19,944.5
```

## 手動測試

推送到 GitHub 後可手動觸發：

1. 進入 Actions 標籤
2. 選擇 "競拍計算機 - 每日監控"
3. 點擊 "Run workflow → Run workflow"

## 日誌查看

執行完成後，日誌會儲存為 artifact，保留 7 天。

檢查位置：
`Actions → 執行紀錄 → 選擇執行 → Artifacts → monitor-logs.zip`

## 常見問題

### ❓ 為什麼沒有收到通知？

1. 檢查 Secrets 是否正確設置
2. 查看 GitHub Actions 日誌確認執行是否成功
3. 檢查信箱的垃圾郵件文件夾
4. 確保有進行中的拍賣標案符合條件

### ❓ 如何更改執行時間？

編輯 `.github/workflows/auction-monitor.yml`，修改 cron 表達式：

```yaml
schedule:
  - cron: '0 1 * * *'  # UTC 時間
```

**設置為台灣時間的參考：**
- 9 點 (UTC+8) = `cron: '0 1 * * *'`（UTC 01:00）
- 18 點 (UTC+8) = `cron: '0 10 * * *'`（UTC 10:00）
- 21 點 (UTC+8) = `cron: '0 13 * * *'`（UTC 13:00）

### ❓ 如何只監控特定股票？

編輯 `cmd/monitor/main.go` 的 `analyzeAuction` 函數，添加股票代號篩選：

```go
// 在 fetchAuctions() 後添加
if auction["證券代號"] != "2330" {  // 只監控台積電
    continue
}
```

## 支持的市場類型

系統自動根據拍賣數據中的「發行市場」判斷股票類型並取得正確的股價：

| 市場類型 | 代號格式 | 說明 |
|--------|--------|------|
| **集中交易市場** | `tse_XXXX.tw` | 上市股票（台灣證券交易所） |
| **櫃檯中心市場** | `otc_XXXX.tw` | 上櫃股票（證券櫃檯中心） |
| **興櫃市場** | `otc_XXXX.tw` | 興櫃股票（首日上市前的試水溫）|

系統會自動：
1. 讀取拍賣公告中的「發行市場」字段
2. 決定正確的 API 前綴（tse 或 otc）
3. 查詢最新股價
4. 計算推薦投標價

## 工作流程架構

```
┌─────────────────┐
│ GitHub Actions  │
│   (每天 9 點)   │
└────────┬────────┘
         │
    ┌────▼─────────────┐
    │ 1. 編譯程序       │
    │ 2. 執行監控      │
    │ 3. 發送 Email    │
    └────┬─────────────┘
         │
    ┌────▼──────────────────┐
    │ • 拉取拍賣清單         │
    │ • 過濾進行中標案       │
    │ • 判斷股票市場         │
    │   (上市/上櫃/興櫃)    │
    │ • 取得現股價          │
    │ • 計算反推投標價      │
    │ • 計算淨利潤          │
    │ • 判斷是否符合條件    │
    └────┬──────────────────┘
         │
    ┌────▼───────────┐
    │ Email 通知     │
    │ HTML 格式      │
    └────────────────┘
```

## 進阶配置

### 多個 Email 接收者

修改 `.github/workflows/auction-monitor.yml`：

```yaml
- name: 執行監控檢查
  env:
    EMAIL_TO: ${{ secrets.EMAIL_TO }},${{ secrets.EMAIL_TO_CC }}
```

### 同時發送 Discord 通知

在 `cmd/monitor/main.go` 的 `sendEmailNotification` 後添加：

```go
if discordWebhook := os.Getenv("DISCORD_WEBHOOK"); discordWebhook != "" {
    sendDiscordNotification(results, discordWebhook)
}
```

## 文件說明

```
競拍計算機/
├── .github/
│   └── workflows/
│       └── auction-monitor.yml      # GitHub Action 工作流程
├── cmd/
│   └── monitor/
│       └── main.go                  # 監控程序主邏輯
├── main.go                          # 本地伺服器
├── index.html                       # 前端介面
├── auction.json                     # 數據文件
└── go.mod                           # Go Module 定義
```

## 許可證

MIT License
