//go:build desktop && darwin

package winview

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// 窗口子进程是裸可执行文件(主 .app 为 LSUIElement 不占 Dock),
// 需要自己升级为常规前台应用并挂上 Dock 图标:窗口开着 Dock 有
// 图标,子进程退出图标自然消失。
static void tongtu_setup_app(const void *icon, int len) {
	NSApplication *app = [NSApplication sharedApplication];
	[app setActivationPolicy:NSApplicationActivationPolicyRegular];
	if (icon && len > 0) {
		NSData *data = [NSData dataWithBytes:icon length:len];
		NSImage *img = [[NSImage alloc] initWithData:data];
		if (img) [app setApplicationIconImage:img];
	}
}

static void tongtu_activate(void) {
	[[NSApplication sharedApplication] activateIgnoringOtherApps:YES];
	NSWindow *win = [[NSApplication sharedApplication] mainWindow];
	if (!win) {
		NSArray *wins = [[NSApplication sharedApplication] windows];
		if ([wins count] > 0) win = wins[0];
	}
	[win makeKeyAndOrderFront:nil];
	[win deminiaturize:nil];
}
*/
import "C"

import (
	_ "embed"
	"errors"
	"runtime"
	"unsafe"

	webview "github.com/webview/webview_go"
)

//go:embed dockicon.png
var dockIcon []byte

func init() {
	// webview/AppKit 要求事件循环在主线程;锁在 init 保证 main goroutine 不漂移
	runtime.LockOSThread()
}

type window struct{ wv webview.WebView }

// Open 创建窗口并加载 url;之后调用 Run 进入事件循环。
func Open(url, title string, width, height int) (Window, error) {
	wv := webview.New(false)
	if wv == nil {
		return nil, errors.New("创建 WKWebView 失败")
	}
	wv.SetTitle(title)
	wv.SetSize(width, height, webview.HintNone)
	wv.Navigate(url)
	C.tongtu_setup_app(unsafe.Pointer(&dockIcon[0]), C.int(len(dockIcon)))
	C.tongtu_activate()
	return &window{wv: wv}, nil
}

func (w *window) Run()              { w.wv.Run() }
func (w *window) Terminate()        { w.wv.Terminate() }
func (w *window) Dispatch(f func()) { w.wv.Dispatch(f) }
func (w *window) Raise()            { C.tongtu_activate() }
