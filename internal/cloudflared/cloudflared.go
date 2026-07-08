// Package cloudflared 负责 cloudflared 二进制的探测、可选自动下载,
// 以及作为子进程运行隧道(带自动重启)。
package cloudflared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// binDir 是自动下载的 cloudflared 存放目录。
func binDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tongtu/bin"
	}
	return filepath.Join(home, ".tongtu", "bin")
}

// Find 返回可用的 cloudflared 路径:先查 PATH,再查通途自己的下载目录。
// 都找不到时返回带安装指引的错误。
func Find() (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	p := filepath.Join(binDir(), exeName())
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		return p, nil
	}
	return "", errors.New(`未找到 cloudflared,请先安装:
  macOS:  brew install cloudflared
  Linux:  参考 https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
或加 -auto-install 让通途自动下载官方二进制到 ~/.tongtu/bin/`)
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// downloadURL 返回当前平台对应的官方 release 下载地址。
func downloadURL() (string, error) {
	base := "https://github.com/cloudflare/cloudflared/releases/latest/download/"
	switch runtime.GOOS {
	case "darwin":
		// macOS 发布的是 tgz,统一走 amd64/arm64 命名
		return base + "cloudflared-darwin-" + runtime.GOARCH + ".tgz", nil
	case "linux":
		switch runtime.GOARCH {
		case "amd64", "arm64", "arm", "386":
			return base + "cloudflared-linux-" + runtime.GOARCH, nil
		}
	case "windows":
		if runtime.GOARCH == "amd64" {
			return base + "cloudflared-windows-amd64.exe", nil
		}
	}
	return "", fmt.Errorf("暂不支持自动下载 %s/%s 平台的 cloudflared,请手动安装", runtime.GOOS, runtime.GOARCH)
}

// Install 下载官方 cloudflared 到 ~/.tongtu/bin/ 并返回其路径。
// macOS 的发布包是 tgz,交给系统 tar 解压,避免引入解压依赖。
func Install(ctx context.Context) (string, error) {
	u, err := downloadURL()
	if err != nil {
		return "", err
	}
	dir := binDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, exeName())

	log.Printf("正在下载 cloudflared: %s", u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 cloudflared: HTTP %d", resp.StatusCode)
	}

	if runtime.GOOS == "darwin" {
		// tgz:落盘后用系统 tar 解出 cloudflared
		tmp := dst + ".tgz"
		if err := saveTo(tmp, resp.Body); err != nil {
			return "", err
		}
		defer os.Remove(tmp)
		cmd := exec.CommandContext(ctx, "tar", "-xzf", tmp, "-C", dir, "cloudflared")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("解压 cloudflared: %v: %s", err, out)
		}
	} else {
		if err := saveTo(dst, resp.Body); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return "", err
	}
	log.Printf("cloudflared 已安装到 %s", dst)
	return dst, nil
}

func saveTo(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Run 以子进程方式运行 `cloudflared tunnel run --token <token>`,
// 进程意外退出时指数退避自动重启(上限 30s),直到 ctx 取消。
func Run(ctx context.Context, bin, token string) error {
	backoff := time.Second
	for {
		start := time.Now()
		err := runOnce(ctx, bin, token)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 稳定运行超过 1 分钟后重置退避
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Printf("cloudflared 退出: %v,%s 后重启", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func runOnce(ctx context.Context, bin, token string) error {
	cmd := exec.CommandContext(ctx, bin,
		"tunnel", "run",
		"--token", token,
		"--no-autoupdate",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// ctx 取消时先发 SIGTERM 让 cloudflared 优雅断开,超时再 SIGKILL
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}
