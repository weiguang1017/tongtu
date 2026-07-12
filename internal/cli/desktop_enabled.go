//go:build desktop

package cli

import (
	"context"
	"flag"

	"tongtu/internal/desktop"
)

func cmdDesktop(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("desktop", flag.ExitOnError)
	hidden := fs.Bool("hidden", false, "静默启动到托盘,不自动打开窗口(开机自启用)")
	addr := fs.String("addr", "127.0.0.1:7080", "面板监听地址(桌面模式仅本机)")
	fs.Parse(args) //nolint:errcheck
	return desktop.Run(ctx, desktop.Options{Hidden: *hidden, Addr: *addr})
}

func cmdWindow(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("window", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:7080/", "要加载的面板地址")
	title := fs.String("title", "通途", "窗口标题")
	width := fs.Int("w", 1100, "窗口宽度")
	height := fs.Int("h", 760, "窗口高度")
	fs.Parse(args) //nolint:errcheck
	return desktop.RunWindow(*url, *title, *width, *height)
}
