package web

import (
	"log"
	"os/exec"
	"runtime"
)

// openBrowser 尽力用系统默认浏览器打开 url;失败只提示,不影响面板运行。
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("未能自动打开浏览器(%v),请手动访问 %s", err, url)
		return
	}
	go cmd.Wait() //nolint:errcheck // 回收子进程,避免僵尸
}
