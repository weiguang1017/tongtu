//go:build desktop && windows

package desktop

import (
	_ "embed"

	"fyne.io/systray"
)

// Windows 托盘要求 ICO 格式(含 16/32 两档)
//
//go:embed icons/tray.ico
var trayIcon []byte

func setTrayIcon() {
	systray.SetIcon(trayIcon)
}
