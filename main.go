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
	// 【新增】释放鼠标捕获，用于拖拽
	procReleaseCapture = user32.NewProc("ReleaseCapture")
)

const (
	GWL_STYLE        = -16
	WS_CAPTION       = 0x00C00000
	WS_THICKFRAME    = 0x00040000
	SWP_FRAMECHANGED = 0x0020
	WM_SYSCOMMAND    = 0x0112
	SC_MINIMIZE      = 0xF020
	// 【新增】模拟点击标题栏移动窗口的指令 (SC_MOVE + HTCAPTION)
	SC_DRAG_MOVE = 0xF012
)

type PlayerBridge struct {
	w webview.WebView
}

// WinMove 【新增】核心拖拽函数：调用系统指令移动窗口
func (p *PlayerBridge) WinMove() {
	hwnd := p.w.Window()
	// 1. 释放当前的鼠标捕获（必须先做这步）
	procReleaseCapture.Call()
	// 2. 发送“点击标题栏”的消息，让系统接管移动
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
	procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0,
		0x0020|0x0001|0x0002|0x0004)
}

func (p *PlayerBridge) WinMin() {
	hwnd := p.w.Window()
	procSendMessage.Call(uintptr(hwnd), uintptr(WM_SYSCOMMAND), uintptr(SC_MINIMIZE), 0)
}

func (p *PlayerBridge) WinClose() {
	p.w.Terminate()
}

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

func (p *PlayerBridge) Log(msg string) {
	fmt.Println("Frontend:", msg)
}

func main() {
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

	w := webview.New(true)
	defer w.Destroy()

	w.SetTitle("MoYu Player")
	w.SetSize(800, 600, webview.HintNone)

	bridge := &PlayerBridge{w: w}
	w.Bind("toggleMode", bridge.ToggleMode)
	w.Bind("goLog", bridge.Log)
	w.Bind("winMin", bridge.WinMin)
	w.Bind("winClose", bridge.WinClose)
	w.Bind("setTop", bridge.SetAlwaysOnTop)

	// 【新增绑定】
	w.Bind("winMove", bridge.WinMove)

	htmlContent, _ := content.ReadFile("index.html")
	finalHTML := strings.Replace(string(htmlContent), "{{PORT}}", fmt.Sprintf("%d", port), -1)

	w.SetHtml(finalHTML)

	w.Run()
}
