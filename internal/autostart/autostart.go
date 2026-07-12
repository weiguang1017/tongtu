// Package autostart 实现「开机自启」的安装/卸载/查询,三平台:
// macOS 写 ~/Library/LaunchAgents plist,Windows 写注册表 Run 键,
// Linux 写 ~/.config/autostart .desktop。自启命令统一为
// <自身可执行文件> --hidden,即静默启动到系统托盘。
//
// 纯 Go、无 desktop 构建标签,便于 headless 构建同样编译与单测。
package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

// Enable 安装开机自启;返回的 warning 非空时表示已生效但有需要用户
// 留意的情况(如与旧版 LaunchAgent 并存)。
func Enable() (warning string, err error) {
	exe, err := selfExe()
	if err != nil {
		return "", err
	}
	return enable(exe)
}

// Disable 卸载开机自启;未安装时静默成功。
func Disable() error { return disable() }

// Enabled 报告开机自启当前是否已安装。
func Enabled() (bool, error) { return enabled() }

// selfExe 返回自身可执行文件的绝对路径。
func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	return abs, nil
}
