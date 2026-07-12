//go:build desktop && windows

package winview

import (
	"errors"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procShowWindow      = user32.NewProc("ShowWindow")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	swRestore           = uintptr(9) // SW_RESTORE
)

type window struct{ wv webview2.WebView }

// Open 创建窗口并加载 url;之后调用 Run 进入事件循环。
// go-webview2 纯 Go 实现,依赖系统 WebView2 Runtime(Win10 21H1+ 自带)。
func Open(url, title string, width, height int) (Window, error) {
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  uint(width),
			Height: uint(height),
			Center: true,
			// IconId 2 = go-winres 生成的 syso 里 APP 图标之后的第一个 RT_GROUP_ICON
			IconId: 2,
		},
	})
	if wv == nil {
		return nil, errors.New("创建 WebView2 窗口失败(请安装 Microsoft Edge WebView2 Runtime)")
	}
	wv.Navigate(url)
	return &window{wv: wv}, nil
}

func (w *window) Run()              { w.wv.Run() }
func (w *window) Terminate()        { w.wv.Terminate() }
func (w *window) Dispatch(f func()) { w.wv.Dispatch(f) }

func (w *window) Raise() {
	hwnd := uintptr(w.wv.Window())
	procShowWindow.Call(hwnd, swRestore)     //nolint:errcheck
	procSetForeground.Call(hwnd)             //nolint:errcheck
}
