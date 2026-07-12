//go:build desktop

package desktop

import (
	"bufio"
	"os"
	"strings"
	"time"

	"tongtu/internal/winview"
)

// RunWindow 是窗口子进程(tongtu window)的主体:创建 WebView 窗口并
// 进入事件循环,同时监听 stdin 生命线 —— 读到 "raise" 置前窗口,读到
// EOF(父进程退出)即结束自己。必须在主线程调用(经 cli 从 main 直达)。
func RunWindow(url, title string, width, height int) error {
	w, err := winview.Open(url, title, width, height)
	if err != nil {
		return err
	}

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "raise" {
				w.Dispatch(w.Raise)
			}
		}
		// EOF:父进程已死。优雅结束事件循环;若 Dispatch 因事件循环
		// 未就绪被丢弃,兜底硬退出(窗口进程无状态,直接退无副作用)。
		w.Dispatch(w.Terminate)
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	w.Run()
	return nil
}
