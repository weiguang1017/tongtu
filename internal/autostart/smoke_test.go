package autostart

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestEnableDisable 用临时 HOME 验证自启项的写入/查询/删除(darwin/linux;
// windows 走注册表,不适合在单测里动真实 HKCU,由 CI 桌面构建覆盖编译即可)。
func TestEnableDisable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 自启走注册表,单测不动真实 HKCU")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	if on, err := Enabled(); err != nil || on {
		t.Fatalf("初始状态应为未启用, got on=%v err=%v", on, err)
	}
	if _, err := Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if on, err := Enabled(); err != nil || !on {
		t.Fatalf("Enable 后应为已启用, got on=%v err=%v", on, err)
	}

	// 自启命令必须带 --hidden(静默到托盘)
	home := os.Getenv("HOME")
	candidates := []string{
		filepath.Join(home, "Library", "LaunchAgents", "com.tongtu.desktop.plist"),
		filepath.Join(home, ".config", "autostart", "tongtu.desktop"),
	}
	found := false
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		found = true
		if !bytes.Contains(data, []byte("--hidden")) {
			t.Fatalf("%s 缺少 --hidden 参数:\n%s", p, data)
		}
	}
	if !found {
		t.Fatal("未找到自启配置文件")
	}

	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if on, _ := Enabled(); on {
		t.Fatal("Disable 后仍为已启用")
	}
	if err := Disable(); err != nil {
		t.Fatalf("重复 Disable 应静默成功: %v", err)
	}
}
