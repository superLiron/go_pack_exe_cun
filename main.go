// main.go
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Config 配置结构体
type Config struct {
	Webhook   string   `json:"webhook"`             // 企业微信 webhook 地址
	Message   string   `json:"message"`             // 要发送的消息内容
	SendDays  []int    `json:"send_days"`           // 发送的星期（0=周日, 1=周一, ..., 6=周六）
	SendTimes []string `json:"send_times"`          // 发送的时间列表，格式 "HH:MM"
}

const configFileName = "config.txt"

var testMode = flag.Bool("test", false, "测试发送一次消息")

func main() {
	flag.Parse()

	if *testMode {
		fmt.Println("📤 正在执行测试发送...")
		testSend()
		return
	}

	cfg := loadConfig()
	fmt.Println("✅ 企业微信定时提醒器已启动")
	fmt.Printf("📌 Webhook: %s\n", maskWebhook(cfg.Webhook))
	fmt.Printf("📝 消息内容: %s\n", cfg.Message)
	fmt.Printf("📅 发送星期: %v (0=周日, 1=周一...)\n", cfg.SendDays)
	fmt.Printf("⏰ 发送时间: %v\n", cfg.SendTimes)
	fmt.Println("ℹ️  每分钟检查一次，按 Ctrl+C 退出程序。")

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// 立即检查一次
	checkAndSend(cfg)

	for range ticker.C {
		checkAndSend(cfg)
	}
}

// loadConfig 从 config.txt 加载配置，并自动处理 GBK 编码
func loadConfig() *Config {
	data, err := os.ReadFile(configFileName)
	if err != nil {
		createExampleConfig()
		log.Fatalf("❌ 未找到配置文件 '%s'，已生成示例文件，请编辑后重新运行！", configFileName)
	}

	// 尝试将 GBK 转为 UTF-8；如果失败，保持原数据（假设已是 UTF-8）
	if utf8Data, ok := tryConvertGBKToUTF8(data); ok {
		data = utf8Data
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("❌ 配置文件格式错误: %v", err)
	}

	// 基本校验
	if cfg.Webhook == "" {
		log.Fatal("❌ 配置错误：webhook 不能为空")
	}
	if cfg.Message == "" {
		log.Fatal("❌ 配置错误：message 不能为空")
	}
	if len(cfg.SendDays) == 0 || len(cfg.SendTimes) == 0 {
		log.Fatal("❌ 配置错误：send_days 和 send_times 至少各需一个值")
	}

	for _, d := range cfg.SendDays {
		if d < 0 || d > 6 {
			log.Fatalf("❌ 星期值必须在 0~6 之间（0=周日），当前值: %d", d)
		}
	}

	for _, t := range cfg.SendTimes {
		if _, err := time.Parse("15:04", t); err != nil {
			log.Fatalf("❌ 时间格式错误: '%s'，应为 'HH:MM'（如 09:00）", t)
		}
	}

	return &cfg
}

// tryConvertGBKToUTF8 尝试将字节流从 GBK 转为 UTF-8
// 成功返回 (utf8Bytes, true)，失败返回 (original, false)
func tryConvertGBKToUTF8(data []byte) ([]byte, bool) {
	decoder := simplifiedchinese.GBK.NewDecoder()
	utf8Data, n, err := transform.Bytes(decoder, data)
	if err != nil || n == 0 {
		return data, false // 转换失败，原样返回
	}
	return utf8Data, true
}

// createExampleConfig 生成 UTF-8 编码的示例配置文件
func createExampleConfig() {
	example := `{
  "webhook": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=你的-key",
  "message": "设备运行正常",
  "send_days": [1, 2, 3, 4, 5],
  "send_times": ["09:00", "14:00"]
}
`
	_ = os.WriteFile(configFileName, []byte(example), 0644)
}

// checkAndSend 检查当前时间是否匹配配置，若匹配则发送
func checkAndSend(cfg *Config) {
	now := time.Now()
	weekday := int(now.Weekday())        // 0=Sunday, 1=Monday, ..., 6=Saturday
	timeStr := now.Format("15:04")       // "09:00"

	dayMatch := false
	for _, d := range cfg.SendDays {
		if d == weekday {
			dayMatch = true
			break
		}
	}
	if !dayMatch {
		return
	}

	timeMatch := false
	for _, t := range cfg.SendTimes {
		if t == timeStr {
			timeMatch = true
			break
		}
	}
	if !timeMatch {
		return
	}

	fmt.Printf("[%s] ⏰ 到点！发送消息: %s\n", timeStr, cfg.Message)
	sendToWechat(cfg.Webhook, cfg.Message)
}

// sendToWechat 发送消息到企业微信（跳过证书验证）
func sendToWechat(webhook, msg string) {
	body := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]string{
			"content": msg,
		},
	}
	jsonBody, _ := json.Marshal(body)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := client.Post(webhook, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		fmt.Printf("❌ 网络错误: %v\n", err)
		return
	}
	defer resp.Body.Close()

	result, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 {
		var res map[string]interface{}
		json.Unmarshal(result, &res)
		if code, ok := res["errcode"].(float64); ok && code == 0 {
			fmt.Println("✅ 企业微信消息发送成功！")
		} else {
			fmt.Printf("❌ 企业微信返回错误: %s\n", string(result))
		}
	} else {
		fmt.Printf("❌ HTTP 错误: %d - %s\n", resp.StatusCode, string(result))
	}
}

// testSend 执行一次测试发送
func testSend() {
	cfg := loadConfig()
	sendToWechat(cfg.Webhook, cfg.Message)
}

// maskWebhook 隐藏 webhook 的 key 部分
func maskWebhook(url string) string {
	if i := strings.Index(url, "key="); i != -1 {
		return url[:i+4] + "******"
	}
	return url
}
