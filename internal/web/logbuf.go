package web

import (
	"strings"
	"sync"
)

// logRing 保存最近的日志行,供 /api/logs 增量拉取。
// 实现 io.Writer,可同时挂到 log 输出与 cloudflared 子进程输出上。
type logRing struct {
	mu    sync.Mutex
	max   int      // 最多保留的行数
	first uint64   // lines[0] 的全局序号
	lines []string // 环形窗口内的行
	part  strings.Builder // 尚未凑满一行的残余字节
}

func newLogRing(max int) *logRing { return &logRing{max: max} }

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			r.push(r.part.String())
			r.part.Reset()
		} else {
			r.part.WriteByte(b)
		}
	}
	return len(p), nil
}

func (r *logRing) push(line string) {
	if len(r.lines) >= r.max {
		drop := len(r.lines) - r.max + 1
		r.lines = append([]string(nil), r.lines[drop:]...)
		r.first += uint64(drop)
	}
	r.lines = append(r.lines, line)
}

// since 返回全局序号 >= after 的行,以及下一次增量拉取应传入的序号。
func (r *logRing) since(after uint64) (next uint64, out []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.first + uint64(len(r.lines))
	if after < r.first {
		after = r.first
	}
	if after > end {
		after = end
	}
	return end, append([]string(nil), r.lines[after-r.first:]...)
}
