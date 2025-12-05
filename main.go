package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	_ "github.com/go-sql-driver/mysql" // MySQL 驱动
	webview "github.com/webview/webview_go"
)

//go:embed index.html
var content embed.FS

// --- 配置区域 ---
const (
	SecretKey = "MySuperSecretKey1234567890123456" // 本地文件加密密钥 (32字节)
	AppName   = "MyVideoPlayer"
)

// --- 安全配置 (防反编译混淆) ---

// 混淆密钥 (必须与生成工具中的一致)
var xorKey = []byte("MyObfuscationKey2025")

// ⚠️⚠️⚠️ [重要] 保持原有的数据库配置 ⚠️⚠️⚠️
var dbDsnSecret = []byte{
	0x2c, 0x09, 0x3f, 0x3d, 0x0a, 0x1c, 0x10, 0x06, 0x0f, 0x07,
	0x0c, 0x1c, 0x54, 0x19, 0x06, 0x3c, 0x41, 0x08, 0x60, 0x4d,
	0x79, 0x1f, 0x1f, 0x3a, 0x0b, 0x16, 0x19, 0x19, 0x31, 0x34,
	0x1d, 0x0c, 0x1e, 0x63, 0x54, 0x41, 0x0b, 0x1e, 0x03, 0x1b,
	0x7f, 0x4b, 0x7b, 0x4c, 0x54, 0x46, 0x4a, 0x59, 0x53, 0x47,
	0x5a, 0x5f, 0x58, 0x62, 0x4a, 0x00, 0x58, 0x4a, 0x41, 0x0a,
	0x2e, 0x11, 0x2e, 0x10, 0x15, 0x10, 0x07, 0x5e, 0x14, 0x00,
	0x0f, 0x57, 0x03, 0x29, 0x51, 0x5f, 0x42, 0x51, 0x40, 0x46,
	0x28, 0x2d, 0x26, 0x0f, 0x03, 0x48, 0x27, 0x11, 0x14, 0x11,
	0x4f, 0x03, 0x01, 0x28, 0x58, 0x35, 0x5d, 0x53, 0x53, 0x59,
}

// getDSN 运行时动态解密连接字符串
func getDSN() string {
	result := make([]byte, len(dbDsnSecret))
	for i, b := range dbDsnSecret {
		result[i] = b ^ xorKey[i%len(xorKey)]
	}
	return string(result)
}

// --- Windows API 定义 ---
var (
	user32                    = syscall.NewLazyDLL("user32.dll")
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procGetWindowLong         = user32.NewProc("GetWindowLongW")
	procSetWindowLong         = user32.NewProc("SetWindowLongW")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procSendMessage           = user32.NewProc("SendMessageW")
	procReleaseCapture        = user32.NewProc("ReleaseCapture")
	procShowWindow            = user32.NewProc("ShowWindow")
	procGetAsyncKeyState      = user32.NewProc("GetAsyncKeyState")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	GWL_STYLE     = -16
	WS_CAPTION    = 0x00C00000
	WS_THICKFRAME = 0x00040000
	WM_SYSCOMMAND = 0x0112
	SC_MINIMIZE   = 0xF020
	SC_DRAG_MOVE  = 0xF012
	SW_HIDE       = 0
	SW_RESTORE    = 9
	VK_MENU       = 0x12
	VK_Q          = 0x51

	DWMWA_USE_IMMERSIVE_DARK_MODE = 20
	DWMWA_BORDER_COLOR            = 34
	DWMWA_CAPTION_COLOR           = 35
	DWMWA_TEXT_COLOR              = 36
)

// --- 激活模块结构定义 ---

// LocalLicenseData 本地缓存结构
type LocalLicenseData struct {
	Code       string    `json:"code"`
	ExpireDate time.Time `json:"expire_date"`
	LastCheck  time.Time `json:"last_check"` // 上次联网时间
}

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

// encryptData AES-GCM 加密
func encryptData(data []byte) (string, error) {
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
	ciphertext := aesGCM.Seal(nonce, nonce, data, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptData AES-GCM 解密
func decryptData(encryptedStr string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedStr)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher([]byte(SecretKey))
	if err != nil {
		return nil, err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, cipherText := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return aesGCM.Open(nil, nonce, cipherText, nil)
}

// getHWID 获取简易硬件指纹 (使用第一个有效的 MAC 地址)
func getHWID() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "unknown_device"
	}
	// 拼接所有有效的 MAC 地址，防止单一网卡变动造成识别失败
	var hwids []string
	for _, i := range interfaces {
		// 排除回环地址和未开启的接口
		if i.Flags&net.FlagUp != 0 && len(i.HardwareAddr) > 0 {
			hwids = append(hwids, i.HardwareAddr.String())
		}
	}
	if len(hwids) > 0 {
		// 返回第一个 MAC，或者你可以组合它们
		return hwids[0]
	}
	return "unknown_device"
}

// checkOnlineConnect 联网校验 (核心逻辑)
// checkOnlineConnect 联网校验 (含设备绑定逻辑)
func checkOnlineConnect(code string) (time.Time, bool, string) {
	// 1. 获取当前机器的硬件 ID
	currentHWID := getHWID()
	if currentHWID == "unknown_device" {
		return time.Time{}, false, "无法识别设备硬件信息"
	}

	// 2. 连接数据库
	db, err := sql.Open("mysql", getDSN())
	if err != nil {
		return time.Time{}, false, "数据库连接初始化失败"
	}
	defer db.Close()
	db.SetConnMaxLifetime(10 * time.Second)

	if err := db.Ping(); err != nil {
		return time.Time{}, false, "无法连接验证服务器"
	}

	var expireDate time.Time
	var isBanned int
	var dbDeviceID sql.NullString // 使用 NullString 处理数据库中可能为 NULL 的情况

	// 3. 查询：同时查出 device_id
	query := "SELECT expire_date, is_banned, device_id FROM app_licenses WHERE code = ? LIMIT 1"
	err = db.QueryRow(query, code).Scan(&expireDate, &isBanned, &dbDeviceID)

	if err != nil {
		if err == sql.ErrNoRows {
			return time.Time{}, false, "激活码不存在"
		}
		return time.Time{}, false, "验证服务器错误"
	}

	if isBanned == 1 {
		return time.Time{}, false, "该激活码已被封禁"
	}

	// 4. 设备绑定逻辑核心
	// 情况 A: 数据库中 device_id 为空 (该码第一次使用) -> 执行绑定
	if !dbDeviceID.Valid || dbDeviceID.String == "" {
		updateQuery := "UPDATE app_licenses SET device_id = ? WHERE code = ?"
		_, err := db.Exec(updateQuery, currentHWID, code)
		if err != nil {
			return time.Time{}, false, "设备绑定失败，请重试"
		}
		// 绑定成功，继续后续过期检查...
	} else {
		// 情况 B: 数据库已有记录 -> 检查是否与当前机器一致
		if dbDeviceID.String != currentHWID {
			return time.Time{}, false, "激活失败：该激活码已绑定其他设备"
		}
		// 情况 C: 一致 -> 通过，继续后续过期检查...
	}

	// 5. 检查是否已过期
	if time.Now().After(expireDate.Add(24 * time.Hour)) {
		return expireDate, false, "激活码已过期"
	}

	return expireDate, true, "OK"
}

// saveLocalLicense 更新本地缓存
func saveLocalLicense(code string, expireDate time.Time) error {
	data := LocalLicenseData{
		Code:       code,
		ExpireDate: expireDate,
		LastCheck:  time.Now(),
	}
	jsonData, _ := json.Marshal(data)
	encrypted, err := encryptData(jsonData)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(getLicenseFilePath(), []byte(encrypted), 0644)
}

// --- API 绑定 ---
type LicenseAPI struct{}

// CheckSavedLicense 启动时自动检查
func (l *LicenseAPI) CheckSavedLicense() map[string]interface{} {
	path := getLicenseFilePath()
	fileData, err := ioutil.ReadFile(path)
	if err != nil {
		return map[string]interface{}{"valid": false, "msg": "请输入激活码激活软件"}
	}

	// 1. 解密
	decryptedJson, err := decryptData(string(fileData))
	if err != nil {
		return map[string]interface{}{"valid": false, "msg": "授权文件损坏，请重新激活"}
	}

	var licData LocalLicenseData
	if err := json.Unmarshal(decryptedJson, &licData); err != nil {
		return map[string]interface{}{"valid": false, "msg": "授权数据错误"}
	}

	// 2. 检查缓存是否在 24 小时内
	// 逻辑：如果 (上次检查时间 + 24小时) 晚于 (当前时间)，说明缓存还有效
	cacheValidUntil := licData.LastCheck.Add(24 * time.Hour)
	isCacheValid := time.Now().Before(cacheValidUntil)

	// 3. 检查硬性过期时间
	isLicenseExpired := time.Now().After(licData.ExpireDate.Add(24 * time.Hour))

	if isLicenseExpired {
		return map[string]interface{}{"valid": false, "msg": "授权已过期"}
	}

	// 4. 分支处理
	if isCacheValid {
		// 缓存有效：不连网，直接返回成功
		daysLeft := int(time.Until(licData.ExpireDate.Add(24*time.Hour)).Hours() / 24)
		return map[string]interface{}{
			"valid": true,
			"msg":   fmt.Sprintf("已激活 (离线缓存)，有效期至 %s (剩余 %d 天)", licData.ExpireDate.Format("2006-01-02"), daysLeft),
		}
	} else {
		// 缓存失效：强制联网验证
		fmt.Println("Frontend: Cache expired, verifying online...")
		expDate, valid, msg := checkOnlineConnect(licData.Code)
		if valid {
			// 联网验证成功 -> 更新本地缓存文件的 LastCheck
			saveLocalLicense(licData.Code, expDate)
			daysLeft := int(time.Until(expDate.Add(24*time.Hour)).Hours() / 24)
			return map[string]interface{}{
				"valid": true,
				"msg":   fmt.Sprintf("已激活 (在线校验)，有效期至 %s (剩余 %d 天)", expDate.Format("2006-01-02"), daysLeft),
			}
		} else {
			// 联网验证失败 (可能被封号、过期或断网)
			return map[string]interface{}{"valid": false, "msg": "在线校验失败: " + msg}
		}
	}
}

// Activate 手动激活
func (l *LicenseAPI) Activate(code string) map[string]interface{} {
	code = strings.TrimSpace(code)

	// 激活必须联网
	expDate, valid, msg := checkOnlineConnect(code)

	if valid {
		// 成功 -> 写入本地文件
		err := saveLocalLicense(code, expDate)
		if err != nil {
			return map[string]interface{}{"success": false, "msg": "写入授权文件失败"}
		}
		return map[string]interface{}{
			"success": true,
			"msg":     fmt.Sprintf("激活成功！有效期至: %s", expDate.Format("2006-01-02")),
		}
	}
	return map[string]interface{}{"success": false, "msg": msg}
}

// --- 播放器 UI 桥接 ---
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

// SetTitleColor 设置颜色并强制刷新样式
// 修复后的 SetTitleColor 函数
func (p *PlayerBridge) SetTitleColor(hex string) {
	hwnd := p.w.Window()
	hex = strings.TrimPrefix(hex, "#")
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return
	}
	r := uint32(v>>16) & 0xFF
	g := uint32(v>>8) & 0xFF
	b := uint32(v) & 0xFF
	bgColor := r | (g << 8) | (b << 16)
	var textColor uint32
	var isDark uint32 = 0

	// 简单的深浅色判断
	if (float64(r)*0.299 + float64(g)*0.587 + float64(b)*0.114) > 128 {
		textColor = 0x00000000 // 黑色文字
		isDark = 0
	} else {
		textColor = 0x00FFFFFF // 白色文字
		isDark = 1
	}

	ptrBg := uintptr(unsafe.Pointer(&bgColor))
	ptrText := uintptr(unsafe.Pointer(&textColor))
	ptrDark := uintptr(unsafe.Pointer(&isDark))

	// 1. 设置 DWM 属性
	procDwmSetWindowAttribute.Call(uintptr(hwnd), uintptr(DWMWA_CAPTION_COLOR), ptrBg, 4)
	procDwmSetWindowAttribute.Call(uintptr(hwnd), uintptr(DWMWA_BORDER_COLOR), ptrBg, 4)
	procDwmSetWindowAttribute.Call(uintptr(hwnd), uintptr(DWMWA_TEXT_COLOR), ptrText, 4)
	procDwmSetWindowAttribute.Call(uintptr(hwnd), uintptr(DWMWA_USE_IMMERSIVE_DARK_MODE), ptrDark, 4)

	// 2. ⚠️ 强制刷新 Hack
	// 【修复点】：先转为 int，避免常量直接转 uintptr 报错
	gwlStyle := int(GWL_STYLE)

	style, _, _ := procGetWindowLong.Call(uintptr(hwnd), uintptr(gwlStyle))
	currentStyle := int32(style)

	// 如果当前是有标题栏模式，才进行刷新操作
	if currentStyle&WS_CAPTION != 0 {
		// 暂时移除 WS_CAPTION
		// 【修复点】：使用 gwlStyle 变量
		procSetWindowLong.Call(uintptr(hwnd), uintptr(gwlStyle), uintptr(currentStyle&^WS_CAPTION))

		// 立即应用
		procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, 0x0020|0x0001|0x0002|0x0004|0x0010)

		// 立即加回 WS_CAPTION
		// 【修复点】：使用 gwlStyle 变量
		procSetWindowLong.Call(uintptr(hwnd), uintptr(gwlStyle), uintptr(currentStyle))
	}

	// 3. 最终应用更改
	procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, 0x0020|0x0001|0x0002|0x0004|0x0010)
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	dir, _ := os.Getwd()
	fileHandler := http.FileServer(http.Dir(dir))
	go func() { http.Serve(listener, fileHandler) }()
	fmt.Printf("Server started at http://127.0.0.1:%d\n", port)

	w := webview.New(true)
	defer w.Destroy()
	w.SetTitle("邮箱助手")
	w.SetSize(800, 600, webview.HintNone)

	bridge := &PlayerBridge{w: w, isVisible: true}
	licApi := &LicenseAPI{}

	// 绑定 JS 调用
	w.Bind("toggleMode", bridge.ToggleMode)
	w.Bind("goLog", bridge.Log)
	w.Bind("winMin", bridge.WinMin)
	w.Bind("winClose", bridge.WinClose)
	w.Bind("setTop", bridge.SetAlwaysOnTop)
	w.Bind("winMove", bridge.WinMove)
	w.Bind("bossKey", bridge.ToggleVisibility)
	w.Bind("checkLicense", licApi.CheckSavedLicense) // 启动检查（含缓存逻辑）
	w.Bind("activate", licApi.Activate)              // 手动激活（强制联网）
	w.Bind("setTitleColor", bridge.SetTitleColor)

	htmlContent, _ := content.ReadFile("index.html")
	finalHTML := strings.Replace(string(htmlContent), "{{PORT}}", fmt.Sprintf("%d", port), -1)
	w.SetHtml(finalHTML)

	// 快捷键监听线程
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
