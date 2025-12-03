package main

import (
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"

	webview "github.com/webview/webview_go"
)

//go:embed index.html
var content embed.FS

// --- Windows API 定义 ---
var (
	user32            = syscall.NewLazyDLL("user32.dll")
	procGetWindowLong = user32.NewProc("GetWindowLongW")
	procSetWindowLong = user32.NewProc("SetWindowLongW")
	procSetWindowPos  = user32.NewProc("SetWindowPos")
	procSendMessage   = user32.NewProc("SendMessageW")
)

const (
	GWL_STYLE        = -16
	WS_CAPTION       = 0x00C00000 // 标题栏
	WS_THICKFRAME    = 0x00040000 // 可调整大小的边框
	SWP_FRAMECHANGED = 0x0020
	WM_SYSCOMMAND    = 0x0112
	SC_MINIMIZE      = 0xF020
)

// 定义一个结构体用于在JS和Go之间通信
type PlayerBridge struct {
	w webview.WebView
}

// setFrameless 修改窗口样式：true=无边框(移除标题栏), false=恢复默认
func (p *PlayerBridge) setFrameless(frameless bool) {
	hwnd := p.w.Window()

	// 【核心修复点】防止 constant overflow 报错
	// 必须先把常量赋值给变量，再转 uintptr
	index := GWL_STYLE

	// 获取当前样式
	style, _, _ := procGetWindowLong.Call(uintptr(hwnd), uintptr(index))
	newStyle := int32(style)

	if frameless {
		// 移除标题栏和粗边框
		newStyle = newStyle &^ (WS_CAPTION | WS_THICKFRAME)
	} else {
		// 恢复标题栏和粗边框
		newStyle = newStyle | WS_CAPTION | WS_THICKFRAME
	}

	// 应用新样式
	procSetWindowLong.Call(uintptr(hwnd), uintptr(index), uintptr(newStyle))

	// 刷新窗口位置/状态以让样式生效
	procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0,
		0x0020|0x0001|0x0002|0x0004) // SWP_FRAMECHANGED | SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER
}

// WinMin 最小化窗口
func (p *PlayerBridge) WinMin() {
	hwnd := p.w.Window()
	procSendMessage.Call(uintptr(hwnd), uintptr(WM_SYSCOMMAND), uintptr(SC_MINIMIZE), 0)
}

// WinClose 关闭程序
func (p *PlayerBridge) WinClose() {
	p.w.Terminate()
}

// SetAlwaysOnTop 设置窗口置顶状态
func (p *PlayerBridge) SetAlwaysOnTop(isTop bool) {
	hwnd := p.w.Window()

	// 定义变量来存储常量，防止编译器报 overflow 错误
	hwndTopMost := -1   // HWND_TOPMOST
	hwndNoTopMost := -2 // HWND_NOTOPMOST

	var targetOrder int
	if isTop {
		targetOrder = hwndTopMost
	} else {
		targetOrder = hwndNoTopMost
	}

	// 调用 SetWindowPos 修改 Z 序 (不改变大小和位置)
	procSetWindowPos.Call(
		uintptr(hwnd),
		uintptr(targetOrder),
		0, 0, 0, 0,
		0x0003, // SWP_NOMOVE | SWP_NOSIZE
	)
}

// ToggleMode 切换窗口大小：正常模式 vs 摸鱼模式
func (p *PlayerBridge) ToggleMode(isMini bool) {
	if isMini {
		// 1. 先切换到无边框样式
		p.setFrameless(true)
		// 2. 摸鱼模式：极小尺寸 (320x180 16:9)
		p.w.SetSize(320, 180, webview.HintNone)
		p.w.SetTitle("System Update")
	} else {
		// 1. 恢复有边框样式
		p.setFrameless(false)
		// 2. 正常模式：大屏
		p.w.SetSize(800, 600, webview.HintNone)
		p.w.SetTitle("Player")
	}
}

// Log 简单的日志打印
func (p *PlayerBridge) Log(msg string) {
	fmt.Println("Frontend:", msg)
}

func main() {
	// 1. 启动本地文件服务器
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	dir, _ := os.Getwd()
	fileHandler := http.FileServer(http.Dir(dir))

	go func() {
		http.Serve(listener, fileHandler)
	}()

	fmt.Printf("Server started at http://127.0.0.1:%d\n", port)

	// 2. 初始化 WebView
	w := webview.New(true)
	defer w.Destroy()

	w.SetTitle("MoYu Player")
	w.SetSize(800, 600, webview.HintNone)

	// 3. 绑定 Go 函数给 JS 调用
	bridge := &PlayerBridge{w: w}
	w.Bind("toggleMode", bridge.ToggleMode)
	w.Bind("goLog", bridge.Log)
	w.Bind("winMin", bridge.WinMin)
	w.Bind("winClose", bridge.WinClose)
	w.Bind("setTop", bridge.SetAlwaysOnTop) // 绑定置顶函数

	// 4. 注入前端 HTML
	htmlContent, _ := content.ReadFile("index.html")
	finalHTML := strings.Replace(string(htmlContent), "{{PORT}}", fmt.Sprintf("%d", port), -1)

	w.SetHtml(finalHTML)

	w.Run()
}
