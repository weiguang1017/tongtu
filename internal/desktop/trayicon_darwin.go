//go:build desktop && darwin

package desktop

import (
	_ "embed"

	"fyne.io/systray"
)

// 单色模板图:macOS 按菜单栏明暗模式自动着色
//
//go:embed icons/tray_template.png
var trayIcon []byte

func setTrayIcon() {
	systray.SetTemplateIcon(trayIcon, trayIcon)
}
