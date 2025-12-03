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
	"time"

	webview "github.com/webview/webview_go"
)

//go:embed index.html
var content embed.FS

// --- Windows API 定义 ---
var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetWindowLong    = user32.NewProc("GetWindowLongW")
	procSetWindowLong    = user32.NewProc("SetWindowLongW")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procSendMessage      = user32.NewProc("SendMessageW")
	procReleaseCapture   = user32.NewProc("ReleaseCapture")
	procShowWindow       = user32.NewProc("ShowWindow")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
	// 【新增】强制设置前台窗口，确保召唤时在最前面
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

	SW_HIDE    = 0
	SW_SHOW    = 5
	SW_RESTORE = 9 // 假如最小化了，用这个可以还原

	VK_MENU = 0x12 // Alt
	VK_Q    = 0x51 // Q
)

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

// 【关键逻辑】真正的双向开关
func (p *PlayerBridge) ToggleVisibility() {
	hwnd := p.w.Window()

	if p.isVisible {
		// --- 状态：当前显示 -> 执行隐藏 ---
		procShowWindow.Call(uintptr(hwnd), uintptr(SW_HIDE))
		p.isVisible = false
		// fmt.Println("老板键触发：隐藏")
	} else {
		// --- 状态：当前隐藏 -> 执行召唤 ---
		// 使用 SW_RESTORE 确保即使是最小化状态也能弹回来
		procShowWindow.Call(uintptr(hwnd), uintptr(SW_RESTORE))

		// 强制拉到最前台
		procSetForegroundWindow.Call(uintptr(hwnd))

		// 再次确保置顶逻辑（如果之前开启了置顶）
		// 这里简单处理，给它一个普通的显示信号
		procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, 0x0003)

		p.isVisible = true
		// fmt.Println("老板键触发：显示")
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

	w.SetTitle("邮箱助手")
	w.SetSize(800, 600, webview.HintNone)

	bridge := &PlayerBridge{
		w:         w,
		isVisible: true,
	}

	w.Bind("toggleMode", bridge.ToggleMode)
	w.Bind("goLog", bridge.Log)
	w.Bind("winMin", bridge.WinMin)
	w.Bind("winClose", bridge.WinClose)
	w.Bind("setTop", bridge.SetAlwaysOnTop)
	w.Bind("winMove", bridge.WinMove)
	// 依然保留 JS 调用接口，万一你想用鼠标点
	w.Bind("bossKey", bridge.ToggleVisibility)

	htmlContent, _ := content.ReadFile("index.html")
	finalHTML := strings.Replace(string(htmlContent), "{{PORT}}", fmt.Sprintf("%d", port), -1)

	w.SetHtml(finalHTML)

	// --- 全局键盘监听协程 ---
	go func() {
		for {
			// 50ms 检测一次，反应更灵敏
			time.Sleep(50 * time.Millisecond)

			// 检查 Alt 键
			altState, _, _ := procGetAsyncKeyState.Call(uintptr(VK_MENU))
			// 检查 Q 键
			qState, _, _ := procGetAsyncKeyState.Call(uintptr(VK_Q))

			// 0x8000 代表键被按下
			if (altState&0x8000 != 0) && (qState&0x8000 != 0) {
				// 触发切换
				w.Dispatch(func() {
					bridge.ToggleVisibility()
				})

				// 防抖动：按下后强制等待 300ms，防止一次按键触发多次开关
				// 这样你按一下就是一下，不会闪烁
				time.Sleep(300 * time.Millisecond)
			}
		}
	}()

	w.Run()
}
