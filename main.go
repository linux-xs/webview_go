package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	webview "github.com/webview/webview_go"
)

//go:embed index.html
var content embed.FS

// --- 配置区域 ---
const SecretKey = "MySuperSecretKey1234567890123456" // 32字节密钥
const AppName = "MyVideoPlayer"

// --- Windows API 定义 (保留你原有的定义) ---
var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procGetWindowLong       = user32.NewProc("GetWindowLongW")
	procSetWindowLong       = user32.NewProc("SetWindowLongW")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procSendMessage         = user32.NewProc("SendMessageW")
	procReleaseCapture      = user32.NewProc("ReleaseCapture")
	procShowWindow          = user32.NewProc("ShowWindow")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const (
	GWL_STYLE        = -16
	WS_CAPTION       = 0x00C00000
	WS_THICKFRAME    = 0x00040000
	SWP_FRAMECHANGED = 0x0020
	WM_SYSCOMMAND    = 0x0112
	SC_MINIMIZE      = 0xF020
	SC_DRAG_MOVE     = 0xF012
	SW_HIDE          = 0
	SW_SHOW          = 5
	SW_RESTORE       = 9
	VK_MENU          = 0x12 // Alt
	VK_Q             = 0x51 // Q
)

// --- 激活模块工具函数 ---

func getLicenseFilePath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir, _ = os.Getwd()
	}
	appDir := filepath.Join(configDir, AppName)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		os.MkdirAll(appDir, 0755)
	}
	return filepath.Join(appDir, "license.dat")
}

func generateLicense(days int) (string, error) {
	expireDate := time.Now().AddDate(0, 0, days)
	expireDateStr := expireDate.Format("2006-01-02")
	block, err := aes.NewCipher([]byte(SecretKey))
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(expireDateStr), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func verifyLicense(licenseCode string) (time.Time, bool) {
	ciphertext, err := base64.StdEncoding.DecodeString(licenseCode)
	if err != nil {
		return time.Time{}, false
	}
	block, err := aes.NewCipher([]byte(SecretKey))
	if err != nil {
		return time.Time{}, false
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return time.Time{}, false
	}
	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return time.Time{}, false
	}
	nonce, cipherText := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return time.Time{}, false
	}
	expireDateStr := string(plaintext)
	expireTime, err := time.Parse("2006-01-02", expireDateStr)
	if err != nil {
		return time.Time{}, false
	}
	// 允许当天有效 (比较时加上24小时缓冲)
	if time.Now().Before(expireTime.Add(24 * time.Hour)) {
		return expireTime, true
	}
	return expireTime, false
}

// --- 激活 API ---
type LicenseAPI struct{}

func (l *LicenseAPI) CheckSavedLicense() map[string]interface{} {
	path := getLicenseFilePath()
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return map[string]interface{}{"valid": false, "msg": "请输入激活码激活软件"}
	}
	code := string(data)
	expDate, valid := verifyLicense(code)
	if valid {
		daysLeft := int(time.Until(expDate.Add(24*time.Hour)).Hours() / 24)
		return map[string]interface{}{
			"valid": true,
			"msg":   fmt.Sprintf("已激活，有效期至 %s (剩余 %d 天)", expDate.Format("2006-01-02"), daysLeft),
		}
	}
	return map[string]interface{}{"valid": false, "msg": "授权已过期或无效"}
}

func (l *LicenseAPI) Activate(code string) map[string]interface{} {
	code = strings.TrimSpace(code)
	expDate, valid := verifyLicense(code)
	if valid {
		path := getLicenseFilePath()
		ioutil.WriteFile(path, []byte(code), 0644)
		return map[string]interface{}{
			"success": true,
			"msg":     fmt.Sprintf("激活成功！有效期至: %s", expDate.Format("2006-01-02")),
		}
	}
	return map[string]interface{}{"success": false, "msg": "激活码无效"}
}

// --- 播放器桥接 (保留你原有的所有方法) ---
type PlayerBridge struct {
	w         webview.WebView
	isVisible bool
}

func (p *PlayerBridge) WinMove() {
	hwnd := p.w.Window()
	procReleaseCapture.Call()
	procSendMessage.Call(uintptr(hwnd), uintptr(WM_SYSCOMMAND), uintptr(SC_DRAG_MOVE), 0)
}

func (p *PlayerBridge) setFrameless(frameless bool) {
	hwnd := p.w.Window()
	index := GWL_STYLE
	style, _, _ := procGetWindowLong.Call(uintptr(hwnd), uintptr(index))
	newStyle := int32(style)
	if frameless {
		newStyle = newStyle &^ (WS_CAPTION | WS_THICKFRAME)
	} else {
		newStyle = newStyle | WS_CAPTION | WS_THICKFRAME
	}
	procSetWindowLong.Call(uintptr(hwnd), uintptr(index), uintptr(newStyle))
	procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, 0x0020|0x0001|0x0002|0x0004)
}

func (p *PlayerBridge) WinMin() {
	hwnd := p.w.Window()
	procSendMessage.Call(uintptr(hwnd), uintptr(WM_SYSCOMMAND), uintptr(SC_MINIMIZE), 0)
}

func (p *PlayerBridge) WinClose() { p.w.Terminate() }

func (p *PlayerBridge) SetAlwaysOnTop(isTop bool) {
	hwnd := p.w.Window()
	hwndTopMost := -1
	hwndNoTopMost := -2
	var targetOrder int
	if isTop {
		targetOrder = hwndTopMost
	} else {
		targetOrder = hwndNoTopMost
	}
	procSetWindowPos.Call(uintptr(hwnd), uintptr(targetOrder), 0, 0, 0, 0, 0x0003)
}

func (p *PlayerBridge) ToggleMode(isMini bool) {
	if isMini {
		p.setFrameless(true)
		p.w.SetSize(320, 180, webview.HintNone)
		p.w.SetTitle("System Update")
	} else {
		p.setFrameless(false)
		p.w.SetSize(800, 600, webview.HintNone)
		p.w.SetTitle("Player")
	}
}

func (p *PlayerBridge) ToggleVisibility() {
	hwnd := p.w.Window()
	if p.isVisible {
		procShowWindow.Call(uintptr(hwnd), uintptr(SW_HIDE))
		p.isVisible = false
	} else {
		procShowWindow.Call(uintptr(hwnd), uintptr(SW_RESTORE))
		procSetForegroundWindow.Call(uintptr(hwnd))
		procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, 0x0003)
		p.isVisible = true
	}
}

func (p *PlayerBridge) Log(msg string) { fmt.Println("Frontend:", msg) }

// --- 主程序 ---
func main() {
	// 1. 生成模式检测 (必须放在最前面)
	if len(os.Args) == 3 && os.Args[1] == "-gen" {
		days := 365
		fmt.Sscanf(os.Args[2], "%d", &days)
		code, err := generateLicense(days)
		if err != nil {
			fmt.Println("Error:", err)
		} else {
			// 确保有输出
			fmt.Printf("\n--- 激活码 (有效期 %d 天) ---\n%s\n--------------------------\n", days, code)
		}
		return // 生成完直接退出
	}

	// 2. 启动文件服务
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	dir, _ := os.Getwd()
	fileHandler := http.FileServer(http.Dir(dir))
	go func() { http.Serve(listener, fileHandler) }()
	fmt.Printf("Server started at http://127.0.0.1:%d\n", port)

	// 3. 启动 WebView
	w := webview.New(true)
	defer w.Destroy()
	w.SetTitle("邮箱助手")
	w.SetSize(800, 600, webview.HintNone)

	// 4. 绑定功能
	bridge := &PlayerBridge{w: w, isVisible: true}
	licApi := &LicenseAPI{}

	// 绑定你的原有功能
	w.Bind("toggleMode", bridge.ToggleMode)
	w.Bind("goLog", bridge.Log)
	w.Bind("winMin", bridge.WinMin)
	w.Bind("winClose", bridge.WinClose)
	w.Bind("setTop", bridge.SetAlwaysOnTop)
	w.Bind("winMove", bridge.WinMove)
	w.Bind("bossKey", bridge.ToggleVisibility)

	// 绑定新增加的激活功能
	w.Bind("checkLicense", licApi.CheckSavedLicense)
	w.Bind("activate", licApi.Activate)

	// 5. 加载 HTML
	htmlContent, _ := content.ReadFile("index.html")
	finalHTML := strings.Replace(string(htmlContent), "{{PORT}}", fmt.Sprintf("%d", port), -1)
	w.SetHtml(finalHTML)

	// 6. 启动老板键监控协程
	go func() {
		for {
			time.Sleep(50 * time.Millisecond)
			altState, _, _ := procGetAsyncKeyState.Call(uintptr(VK_MENU))
			qState, _, _ := procGetAsyncKeyState.Call(uintptr(VK_Q))
			if (altState&0x8000 != 0) && (qState&0x8000 != 0) {
				w.Dispatch(func() { bridge.ToggleVisibility() })
				time.Sleep(300 * time.Millisecond)
			}
		}
	}()

	w.Run()
}
