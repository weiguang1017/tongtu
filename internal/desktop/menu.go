//go:build desktop

package desktop

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"tongtu/internal/autostart"
	"tongtu/internal/cloudflared"
	"tongtu/internal/config"
	"tongtu/internal/runner"
	"tongtu/internal/web"
)

type menuDeps struct {
	mgr      *runner.Manager
	openWin  func() // 打开/置前原生窗口
	panelURL string
}

// runMenu 构建托盘菜单并启动事件循环(事件循环在后台 goroutine,
// systray 的 ClickedCh 机制要求如此;菜单项操作本身线程安全)。
func runMenu(ctx context.Context, quit context.CancelFunc, d menuDeps) {
	mOpen := systray.AddMenuItem("打开面板", "打开通途管理窗口")
	systray.AddSeparator()
	mStatus := systray.AddMenuItem("连接器:已停止", "")
	mStatus.Disable()
	mToggle := systray.AddMenuItem("启动连接器", "发布全部已启用应用")
	systray.AddSeparator()
	mAutostart := systray.AddMenuItemCheckbox("开机自启", "登录后静默启动到托盘", false)
	if on, err := autostart.Enabled(); err == nil && on {
		mAutostart.Check()
	}
	mBrowser := systray.AddMenuItem("在浏览器中打开", "用系统默认浏览器访问面板")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "停止全部隧道并退出通途")

	var busy atomic.Bool // 连接器启停进行中,防重复点击

	refresh := func() {
		st := d.mgr.Status()
		if busy.Load() {
			return // 启停过程中的文案由操作方设置
		}
		if st.Running {
			published := 0
			for _, g := range st.Groups {
				published += len(g.Published)
			}
			mStatus.SetTitle(fmt.Sprintf("连接器:运行中(%d 个应用已发布)", published))
			mToggle.SetTitle("停止连接器")
		} else {
			mStatus.SetTitle("连接器:已停止")
			mToggle.SetTitle("启动连接器")
		}
	}
	refresh()

	toggle := func() {
		if !busy.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer busy.Store(false)
			if d.mgr.Status().Running {
				mStatus.SetTitle("连接器:正在停止…")
				d.mgr.Stop()
			} else {
				mStatus.SetTitle("连接器:正在启动…")
				if err := startConnector(ctx, d.mgr); err != nil {
					log.Printf("托盘启动连接器失败: %v", err)
					mStatus.SetTitle("启动失败(详见面板日志)")
					time.Sleep(4 * time.Second)
				}
			}
		}()
	}

	toggleAutostart := func() {
		if mAutostart.Checked() {
			if err := autostart.Disable(); err != nil {
				log.Printf("关闭开机自启失败: %v", err)
				return
			}
			mAutostart.Uncheck()
			log.Print("已关闭开机自启")
		} else {
			warning, err := autostart.Enable()
			if err != nil {
				log.Printf("开启开机自启失败: %v", err)
				return
			}
			mAutostart.Check()
			log.Print("已开启开机自启:登录后通途将静默启动到托盘")
			if warning != "" {
				log.Print(warning)
			}
		}
	}

	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				refresh()
			case <-mOpen.ClickedCh:
				d.openWin()
			case <-mToggle.ClickedCh:
				toggle()
			case <-mAutostart.ClickedCh:
				toggleAutostart()
			case <-mBrowser.ClickedCh:
				web.OpenBrowser(d.panelURL)
			case <-mQuit.ClickedCh:
				quit()
				return
			}
		}
	}()
}

// startConnector 与面板「启动」按钮同语义:全量发布已启用应用。
func startConnector(ctx context.Context, mgr *runner.Manager) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	plan, err := runner.Build(cfg, nil)
	if err != nil {
		return err
	}
	if _, err := cloudflared.Find(); err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return mgr.Start(sctx, plan, nil)
}
