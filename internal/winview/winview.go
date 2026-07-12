//go:build desktop

// Package winview 统一封装三平台的原生 WebView 窗口:
// macOS/Linux 走 webview_go(WKWebView / WebKitGTK,CGO),
// Windows 走 go-webview2(WebView2,纯 Go)。
// 仅供窗口子进程(tongtu window)使用,一个进程一个窗口,
// Run 占据主线程直到窗口关闭。
package winview

// Window 是一个已创建的原生 WebView 窗口。
type Window interface {
	// Run 进入事件循环,阻塞到窗口关闭或 Terminate。必须在主线程调用。
	Run()
	// Terminate 结束事件循环(须经 Dispatch 在主线程执行)。
	Terminate()
	// Dispatch 把 f 投递到主线程执行;Run 之前投递的任务可能被丢弃。
	Dispatch(f func())
	// Raise 把窗口带到前台(须经 Dispatch 在主线程执行)。
	Raise()
}
