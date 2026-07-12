//go:build desktop && linux

package winview

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

static void tongtu_present(void *win) {
	gtk_window_present(GTK_WINDOW(win));
}
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"

	webview "github.com/webview/webview_go"
)

func init() {
	runtime.LockOSThread()
}

type window struct{ wv webview.WebView }

// Open 创建窗口并加载 url;之后调用 Run 进入事件循环。
func Open(url, title string, width, height int) (Window, error) {
	wv := webview.New(false)
	if wv == nil {
		return nil, errors.New("创建 WebKitGTK 窗口失败(请确认已安装 webkit2gtk)")
	}
	wv.SetTitle(title)
	wv.SetSize(width, height, webview.HintNone)
	wv.Navigate(url)
	return &window{wv: wv}, nil
}

func (w *window) Run()              { w.wv.Run() }
func (w *window) Terminate()        { w.wv.Terminate() }
func (w *window) Dispatch(f func()) { w.wv.Dispatch(f) }
func (w *window) Raise()            { C.tongtu_present(unsafe.Pointer(w.wv.Window())) }
