// Package landing 提供「应用已下线」时的兜底宣传页。
//
// 当某个应用被停用或删除后,其子域名的 DNS 仍指向隧道,请求会落到
// ingress 的兜底规则;兜底规则指向本包在 127.0.0.1 上起的迷你 HTTP
// 服务,访客看到的不再是干巴巴的 404,而是一张介绍通途的宣传页
// (面向谁、适合什么场景、如何下载安装和使用)。
//
// 安全:只监听回环地址,只会经由 cloudflared 隧道被公网访问;
// 页面为纯静态内容,不读配置、不暴露任何本机信息。
package landing

import (
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"time"
)

//go:embed page.html
var pageHTML []byte

//go:embed favicon.svg
var faviconSVG []byte

// Server 是宣传页迷你服务,生命周期跟随连接器总开关。
type Server struct {
	ln  net.Listener
	srv *http.Server
}

// Start 在 127.0.0.1 的随机端口启动宣传页服务。
func Start() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动宣传页服务失败: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(faviconSVG) //nolint:errcheck
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		// 这是别人域名下的占位页,不该进搜索引擎索引
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Write(pageHTML) //nolint:errcheck
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln) //nolint:errcheck
	return &Server{ln: ln, srv: srv}, nil
}

// Service 返回可写入 ingress 兜底规则的服务地址,如 http://127.0.0.1:53211。
func (s *Server) Service() string {
	return "http://" + s.ln.Addr().String()
}

// Close 停止宣传页服务。
func (s *Server) Close() {
	s.srv.Close() //nolint:errcheck
}
