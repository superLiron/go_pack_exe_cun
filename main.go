// main.go
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Config 配置结构体
type Config struct {
	Webhook   string   `json:"webhook"`
	Message   string   `json:"message"`
	SendDays  []int    `json:"send_days"`
	SendTimes []string `json:"send_times"`
}

const configFileName = "config.txt"

var testMode = flag.Bool("test", false, "测试发送一次消息")

// enableQuickEditMode 启用 Windows 控制台的快速编辑模式（支持右键粘贴）
func enableQuickEditMode() {
	kernel32 := windows.NewLazyDLL("kernel32.dll")
	getStdHandle := kernel32.NewProc("GetStdHandle")
	setConsoleMode := kernel32.NewProc("SetConsoleMode")

	// 使用 syscall.STD_INPUT_HANDLE (-10) 获取标准输入句柄
	stdin, _, _ := getStdHandle.Call(uintptr(syscall.STD_INPUT_HANDLE))
	if stdin == 0 {
		return
	}

	// 获取当前控制台输入模式
	var mode uint32
	getConsoleModeProc := kernel32.NewProc("GetConsoleMode")
	ret, _, _ := getConsoleModeProc.Call(stdin, uintptr(unsafe.Pointer(&mode)))
	if ret == 0 {
		return
	}

	// ENABLE_QUICK_EDIT_MODE = 0x0040
	const ENABLE_QUICK_EDIT_MODE = 0x0040
	newMode := mode | ENABLE_QUICK_EDIT_MODE

	// 设置新模式
	setConsoleMode.Call(stdin, uintptr(newMode))
}

func main() {
	// 👇 启用右键粘贴支持（关键！）
	enableQuickEditMode()

	flag.Parse()

	if *testMode {
		fmt.Println("📤 正在执行测试发送...")
		testSend()
		return
	}

	cfg := loadOrPromptConfig()
	fmt.Println("\n✅ 企业微信定时提醒器已启动")
	fmt.Printf("📌 Webhook: %s\n", maskWebhook(cfg.Webhook))
	fmt.Printf("📝 消息内容: %s\n", cfg.Message)
	fmt.Printf("📅 发送星期: %v (0=周日, 1=周一...)\n", cfg.SendDays)
	fmt.Printf("⏰ 发送时间: %v\n", cfg.SendTimes)
	fmt.Println("ℹ️  每分钟检查一次，按 Ctrl+C 退出程序。")

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	checkAndSend(cfg)

	for range ticker.C {
		checkAndSend(cfg)
	}
}

// loadOrPromptConfig 尝试加载 config.txt，若不存在或无效，则交互式引导用户输入
func loadOrPromptConfig() *Config {
	data, err := os.ReadFile(configFileName)
	if err == nil {
		var cfg Config
		if json.Unmarshal(data, &cfg) == nil &&
			cfg.Webhook != "" && cfg.Message != "" &&
			len(cfg.SendDays) > 0 && len(cfg.SendTimes) > 0 {
			valid := true
			for _, d := range cfg.SendDays {
				if d < 0 || d > 6 {
					valid = false
					break
				}
			}
			for _, t := range cfg.SendTimes {
				if _, e := time.Parse("15:04", t); e != nil {
					valid = false
					break
				}
			}
			if valid {
				return &cfg
			}
		}
	}

	fmt.Printf("⚠️ 未找到有效配置文件 '%s'，请按提示输入配置信息：\n\n", configFileName)
	cfg := promptConfigFromUser()
	saveConfig(cfg)
	fmt.Printf("\n✅ 配置已保存到 '%s'，下次启动将自动加载。\n\n", configFileName)
	return cfg
}

// promptConfigFromUser 交互式获取用户输入
func promptConfigFromUser() *Config {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("请输入企业微信 Webhook 地址（示例：https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcd1234...）：\n> ")
	webhook, _ := reader.ReadString('\n')
	webhook = strings.TrimSpace(webhook)
	for webhook == "" {
		fmt.Print("❌ Webhook 不能为空，请重新输入：\n> ")
		webhook, _ = reader.ReadString('\n')
		webhook = strings.TrimSpace(webhook)
	}

	fmt.Print("\n请输入要发送的消息内容（示例：设备运行正常）：\n> ")
	message, _ := reader.ReadString('\n')
	message = strings.TrimSpace(message)
	for message == "" {
		fmt.Print("❌ 消息内容不能为空，请重新输入：\n> ")
		message, _ = reader.ReadString('\n')
		message = strings.TrimSpace(message)
	}

	fmt.Print("\n请输入发送的星期（用英文逗号分隔，0=周日,1=周一,...,6=周六，示例：1,2,3,4,5）：\n> ")
	daysStr, _ := reader.ReadString('\n')
	daysStr = strings.TrimSpace(daysStr)
	var sendDays []int
	for len(sendDays) == 0 {
		if daysStr == "" {
			fmt.Print("❌ 发送星期不能为空，请重新输入（示例：1,2,3）：\n> ")
			daysStr, _ = reader.ReadString('\n')
			daysStr = strings.TrimSpace(daysStr)
			continue
		}
		parts := strings.Split(daysStr, ",")
		sendDays = nil
		valid := true
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			d, err := strconv.Atoi(part)
			if err != nil || d < 0 || d > 6 {
				fmt.Printf("❌ 星期值必须是 0~6 的整数（0=周日），当前输入包含无效值：%s\n", part)
				valid = false
				break
			}
			sendDays = append(sendDays, d)
		}
		if !valid || len(sendDays) == 0 {
			fmt.Print("请重新输入（示例：1,3,5）：\n> ")
			daysStr, _ = reader.ReadString('\n')
			daysStr = strings.TrimSpace(daysStr)
		}
	}

	fmt.Print("\n请输入发送的时间（用英文逗号分隔，格式 HH:MM，示例：09:00,14:30）：\n> ")
	timesStr, _ := reader.ReadString('\n')
	timesStr = strings.TrimSpace(timesStr)
	var sendTimes []string
	for len(sendTimes) == 0 {
		if timesStr == "" {
			fmt.Print("❌ 发送时间不能为空，请重新输入（示例：09:00）：\n> ")
			timesStr, _ = reader.ReadString('\n')
			timesStr = strings.TrimSpace(timesStr)
			continue
		}
		parts := strings.Split(timesStr, ",")
		sendTimes = nil
		valid := true
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, err := time.Parse("15:04", part); err != nil {
				fmt.Printf("❌ 时间格式错误，应为 HH:MM（如 09:00），当前值：%s\n", part)
				valid = false
				break
			}
			sendTimes = append(sendTimes, part)
		}
		if !valid || len(sendTimes) == 0 {
			fmt.Print("请重新输入（示例：09:00,15:00）：\n> ")
			timesStr, _ = reader.ReadString('\n')
			timesStr = strings.TrimSpace(timesStr)
		}
	}

	return &Config{
		Webhook:   webhook,
		Message:   message,
		SendDays:  sendDays,
		SendTimes: sendTimes,
	}
}

// saveConfig 将配置保存为 UTF-8 编码的 config.txt
func saveConfig(cfg *Config) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Fatalf("❌ 无法生成配置文件: %v", err)
	}
	err = os.WriteFile(configFileName, data, 0644)
	if err != nil {
		log.Fatalf("❌ 无法保存配置文件 '%s': %v", configFileName, err)
	}
}

// checkAndSend 检查当前时间是否匹配配置，若匹配则发送
func checkAndSend(cfg *Config) {
	now := time.Now()
	weekday := int(now.Weekday())
	timeStr := now.Format("15:04")

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

// sendToWechat 发送消息到企业微信（禁用证书验证）
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
	cfg := loadOrPromptConfig()
	sendToWechat(cfg.Webhook, cfg.Message)
}

// maskWebhook 隐藏 webhook 的 key 部分
func maskWebhook(url string) string {
	if i := strings.Index(url, "key="); i != -1 {
		return url[:i+4] + "******"
	}
	return url
}
