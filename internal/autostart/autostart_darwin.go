package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

// 与旧版 headless 模板 deploy/com.tongtu.client.plist 区分:
// 桌面自启 KeepAlive 必须为 false,否则用户从托盘退出后会被 launchd 复活。
const agentLabel = "com.tongtu.desktop"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--hidden</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
</dict>
</plist>
`

func agentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

func enable(exe string) (string, error) {
	path, err := agentPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	body := fmt.Sprintf(plistTemplate, agentLabel, exe)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	// 旧版 headless LaunchAgent 并存会导致登录后出现两个连接器抢隧道
	legacy := filepath.Join(filepath.Dir(path), "com.tongtu.client.plist")
	if _, err := os.Stat(legacy); err == nil {
		return fmt.Sprintf("检测到旧版自启配置 %s,建议删除并 launchctl unload,否则登录后会同时运行两份连接器", legacy), nil
	}
	return "", nil
}

func disable() error {
	path, err := agentPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func enabled() (bool, error) {
	path, err := agentPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
