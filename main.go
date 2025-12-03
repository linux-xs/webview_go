package main

import (
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	webview "github.com/webview/webview_go"
)

// 这里不仅可以嵌入HTML，也可以直接写在字符串里
//
//go:embed index.html
var content embed.FS

// 定义一个结构体用于在JS和Go之间通信
type PlayerBridge struct {
	w webview.WebView
}

// ToggleMode 切换窗口大小：正常模式 vs 摸鱼模式
func (p *PlayerBridge) ToggleMode(isMini bool) {
	if isMini {
		// 摸鱼模式：极小，无边框 (具体尺寸取决于系统最小限制，这里设为 160x90 以保持16:9)
		// 如果必须 60x40，可以设置为 60, 40，但可能只能看到几个像素
		p.w.SetSize(160, 90, webview.HintNone)
		p.w.SetTitle("System Update") // 伪装标题
	} else {
		// 正常模式：大屏
		p.w.SetSize(800, 600, webview.HintNone)
		p.w.SetTitle("Player")
	}
}

// OpenFile 让 Go 处理本地文件路径（简单实现）
// 在实际摸鱼中，可以直接拖拽文件进窗口
func (p *PlayerBridge) Log(msg string) {
	fmt.Println("Frontend:", msg)
}

func main() {
	// 1. 启动一个本地文件服务器，用于播放本地视频
	// 这样可以避免浏览器的 file:// 协议安全限制
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// 设置文件服务，允许访问当前运行目录及子目录
	// 注意：生产环境需要考虑安全性，这里为了演示方便直接暴露当前磁盘
	dir, _ := os.Getwd()
	fileHandler := http.FileServer(http.Dir(dir))

	go func() {
		http.Serve(listener, fileHandler)
	}()

	fmt.Printf("Server started at http://127.0.0.1:%d\n", port)

	// 2. 初始化 WebView
	// debug: true 允许右键检查元素，方便调试
	w := webview.New(true)
	defer w.Destroy()

	w.SetTitle("MoYu Player")
	w.SetSize(800, 600, webview.HintNone) // HintNone 表示无边框，更适合摸鱼

	// 3. 绑定 Go 函数给 JS 调用
	bridge := &PlayerBridge{w: w}
	w.Bind("toggleMode", bridge.ToggleMode)
	w.Bind("goLog", bridge.Log)

	// 4. 注入前端 HTML/JS
	// 我们将本地服务器端口注入到 HTML 中，方便 JS 拼接视频地址
	htmlContent, _ := content.ReadFile("index.html")
	finalHTML := strings.Replace(string(htmlContent), "{{PORT}}", fmt.Sprintf("%d", port), -1)

	w.SetHtml(finalHTML)

	w.Run()
}
