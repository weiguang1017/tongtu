package autostart

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "TongTu"

func enable(exe string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", fmt.Errorf("打开注册表 Run 键失败: %w", err)
	}
	defer k.Close()
	if err := k.SetStringValue(valueName, fmt.Sprintf(`"%s" --hidden`, exe)); err != nil {
		return "", err
	}
	return "", nil
}

func disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(valueName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

func enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	if _, _, err := k.GetStringValue(valueName); err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
