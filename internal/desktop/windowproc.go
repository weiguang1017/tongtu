//go:build desktop

package desktop

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// windowProc 管理窗口子进程(tongtu window)。
//
// 生命线协议:父进程持有子进程 stdin 的写端 ——
//   - 写一行 "raise" = 把窗口带到前台;
//   - 关闭写端(或父进程任何死法导致管道关闭)= 子进程读到 EOF 自行退出。
//
// 由此「防多开」「父死子亡」都不需要额外的锁文件或轮询。
type windowProc struct {
	url string

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{} // cmd.Wait 返回后关闭
}

func newWindowProc(url string) *windowProc {
	return &windowProc{url: url}
}

// Show 打开窗口:子进程已存活则只置前,否则 spawn 新子进程。
func (w *windowProc) Show() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cmd != nil {
		select {
		case <-w.done: // 子进程已退出(用户关过窗),走重新 spawn
			w.reset()
		default:
			fmt.Fprintln(w.stdin, "raise") //nolint:errcheck // 管道破裂由 done 兜底
			return
		}
	}

	exe, err := os.Executable()
	if err != nil {
		log.Printf("打开窗口失败:定位自身可执行文件: %v", err)
		return
	}
	cmd := exec.Command(exe, "window", "--url", w.url)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("打开窗口失败: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("打开窗口失败: %v", err)
		return
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait() //nolint:errcheck // 回收子进程即可,关窗非错误
		close(done)
	}()
	w.cmd, w.stdin, w.done = cmd, stdin, done
}

// Close 关闭窗口子进程:关 stdin 触发其 EOF 自杀,超时则强杀。
func (w *windowProc) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd == nil {
		return
	}
	w.stdin.Close() //nolint:errcheck
	select {
	case <-w.done:
	case <-time.After(3 * time.Second):
		w.cmd.Process.Kill() //nolint:errcheck
		<-w.done
	}
	w.reset()
}

// reset 清空运行态(须在 mu 下调用)。
func (w *windowProc) reset() {
	w.cmd, w.stdin, w.done = nil, nil, nil
}
