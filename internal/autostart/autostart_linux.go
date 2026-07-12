package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

const desktopTemplate = `[Desktop Entry]
Type=Application
Name=通途
Comment=天堑变通途,家宽即服务器
Exec="%s" --hidden
Icon=tongtu
Terminal=false
X-GNOME-Autostart-enabled=true
`

func entryPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "tongtu.desktop"), nil
}

func enable(exe string) (string, error) {
	path, err := entryPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	body := fmt.Sprintf(desktopTemplate, exe)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return "", nil
}

func disable() error {
	path, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func enabled() (bool, error) {
	path, err := entryPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
