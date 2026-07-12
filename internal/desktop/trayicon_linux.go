//go:build desktop && linux

package desktop

import (
	_ "embed"

	"fyne.io/systray"
)

//go:embed icons/tray_color.png
var trayIcon []byte

func setTrayIcon() {
	systray.SetIcon(trayIcon)
}
