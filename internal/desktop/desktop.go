//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"fyne.io/systray"

	"tongtu/internal/cloudflared"
	"tongtu/internal/config"
	"tongtu/internal/runner"
	"tongtu/internal/web"
)

// Available 报告本构建是否包含桌面组件。
func Available() bool { return true }

// Run 启动桌面模式:面板 HTTP 服务进 goroutine,systray 占据主线程。
// 必须从 main goroutine 一路同步调用到这里(macOS 要求事件循环在主线程)。
func Run(ctx context.Context, opts Options) error {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:7080"
	}
	panelURL := "http://" + opts.Addr + "/"

	// 单实例:端口上已有一个通途实例就唤起它的窗口,本实例退位
	if pingInstance(opts.Addr) {
		if err := showInstance(opts.Addr); err != nil {
			return fmt.Errorf("通途已在运行(%s),但唤起其窗口失败: %w", opts.Addr, err)
		}
		log.Print("通途已在运行,已唤起既有窗口")
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	wp := newWindowProc(panelURL)
	srv, err := web.New(opts.Addr, "", false, web.WithShowWindow(wp.Show))
	if err != nil {
		return err
	}

	webErr := make(chan error, 1)
	go func() { webErr <- srv.ListenAndServe(ctx) }()
	if err := waitListen(opts.Addr, webErr); err != nil {
		return err
	}

	// 有已启用应用时自动发布(等价开机自启场景下旧 tongtu run 的语义);
	// 失败只记日志,托盘和面板仍可用,用户可手动重试
	go autoPublish(ctx, srv.Manager())

	onReady := func() {
		setTrayIcon()
		systray.SetTooltip("通途 — 天堑变通途")
		runMenu(ctx, cancel, menuDeps{
			mgr:      srv.Manager(),
			openWin:  wp.Show,
			panelURL: panelURL,
		})
		if !opts.Hidden {
			wp.Show()
		}
	}
	onExit := func() {}

	// ctx 取消(托盘退出/SIGTERM)→ 收尾:关窗口子进程 → 等 web 服务
	// 停完隧道并退出 → 结束托盘事件循环
	go func() {
		<-ctx.Done()
		wp.Close()
		select {
		case <-webErr:
		case <-time.After(20 * time.Second):
			log.Print("等待面板服务退出超时,强制退出")
		}
		systray.Quit()
	}()

	systray.Run(onReady, onExit) // 阻塞主线程直到 systray.Quit
	return nil
}

// pingInstance 探测 addr 上是否已有通途面板(GET /api/status 且响应含 running 字段)。
func pingInstance(addr string) bool {
	cli := &http.Client{Timeout: 2 * time.Second}
	resp, err := cli.Get("http://" + addr + "/api/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var st struct {
		Running *bool `json:"running"`
	}
	return resp.StatusCode == http.StatusOK &&
		json.NewDecoder(resp.Body).Decode(&st) == nil && st.Running != nil
}

// showInstance 让既有实例把窗口带到前台。
func showInstance(addr string) error {
	cli := &http.Client{Timeout: 2 * time.Second}
	resp, err := cli.Post("http://"+addr+"/api/desktop/show", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("既有实例响应 %s(可能是 tongtu web 纯面板模式,请直接访问 http://%s)", resp.Status, addr)
	}
	return nil
}

// waitListen 等面板开始监听(窗口子进程加载前必须就绪);监听失败立即返回错误。
func waitListen(addr string, webErr <-chan error) error {
	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-webErr:
			return fmt.Errorf("面板服务启动失败: %w", err)
		case <-deadline:
			return fmt.Errorf("等待面板监听 %s 超时", addr)
		case <-time.After(50 * time.Millisecond):
			if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
				c.Close()
				return nil
			}
		}
	}
}

// autoPublish 启动时自动发布已启用应用:配置里有启用的应用且 cloudflared
// 可用才动手,任何失败只记日志。
func autoPublish(ctx context.Context, mgr *runner.Manager) {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("自动发布跳过:读取配置失败: %v", err)
		return
	}
	enabled := 0
	for _, a := range cfg.Apps {
		if a.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return
	}
	if _, err := cloudflared.Find(); err != nil {
		log.Printf("自动发布跳过:%v", err)
		return
	}
	plan, err := runner.Build(cfg, nil)
	if err != nil {
		log.Printf("自动发布跳过:%v", err)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := mgr.Start(sctx, plan, nil); err != nil {
		log.Printf("自动发布失败(可在面板或托盘手动启动): %v", err)
		return
	}
	log.Printf("已自动发布 %d 个应用", enabled)
}
