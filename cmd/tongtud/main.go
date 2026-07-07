// tongtud 是「通途」的服务端,部署在有公网 IP 的机器上。
//
// 职责:
//  1. 监听控制端口,接受客户端注册(子域名 -> 隧道);
//  2. 监听 80/443,按 Host 里的子域名把公网 HTTP/HTTPS 请求
//     反向代理进对应客户端的隧道;
//  3. HTTPS 由服务端用泛域名证书统一卸载,客户端零证书配置。
package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"
	"sync"
	"time"

	"tongtu/internal/proto"
)

var subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Tunnel 表示一个已注册客户端的隧道。
type Tunnel struct {
	subdomain string
	ctrl      net.Conn

	mu      sync.Mutex
	pending map[string]chan net.Conn // conn_id -> 等待数据连接的通道
	nextID  uint64
	closed  bool
}

// OpenConn 向客户端发起 accept,并等待客户端回拨的数据连接。
func (t *Tunnel) OpenConn(ctx context.Context) (net.Conn, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("tunnel %q closed", t.subdomain)
	}
	t.nextID++
	id := fmt.Sprintf("%s-%d", t.subdomain, t.nextID)
	ch := make(chan net.Conn, 1)
	t.pending[id] = ch
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	if err := proto.WriteMsg(t.ctrl, &proto.Msg{Type: proto.TypeAccept, ConnID: id}); err != nil {
		return nil, fmt.Errorf("notify client: %w", err)
	}

	select {
	case c := <-ch:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("client did not dial back for %s", id)
	}
}

func (t *Tunnel) deliver(id string, c net.Conn) bool {
	t.mu.Lock()
	ch, ok := t.pending[id]
	t.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- c:
		return true
	default:
		return false
	}
}

func (t *Tunnel) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.ctrl.Close()
}

// Registry 管理子域名到隧道的映射。
type Registry struct {
	mu      sync.RWMutex
	tunnels map[string]*Tunnel
}

func NewRegistry() *Registry { return &Registry{tunnels: map[string]*Tunnel{}} }

func (r *Registry) Add(t *Tunnel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tunnels[t.subdomain]; exists {
		return fmt.Errorf("subdomain %q already in use", t.subdomain)
	}
	r.tunnels[t.subdomain] = t
	return nil
}

func (r *Registry) Remove(t *Tunnel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.tunnels[t.subdomain]; ok && cur == t {
		delete(r.tunnels, t.subdomain)
	}
}

func (r *Registry) Get(subdomain string) *Tunnel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tunnels[subdomain]
}

type server struct {
	domain   string // 根域名,如 example.com
	token    string
	registry *Registry
}

// ---- 控制/数据端口 ----

func (s *server) serveControl(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Fatalf("control accept: %v", err)
		}
		go s.handleConn(conn)
	}
}

// handleConn 处理控制端口上的新连接:第一条消息决定它是
// 控制连接(register)还是数据连接(data)。
func (s *server) handleConn(conn net.Conn) {
	r := bufio.NewReader(conn)
	msg, err := proto.ReadMsgTimeout(conn, r, 10*time.Second)
	if err != nil {
		conn.Close()
		return
	}

	if s.token != "" && subtle.ConstantTimeCompare([]byte(msg.Token), []byte(s.token)) != 1 {
		proto.WriteMsg(conn, &proto.Msg{Type: proto.TypeError, Message: "invalid token"}) //nolint:errcheck
		conn.Close()
		return
	}

	switch msg.Type {
	case proto.TypeRegister:
		s.handleRegister(conn, r, msg)
	case proto.TypeData:
		s.handleData(conn, r, msg)
	default:
		conn.Close()
	}
}

func (s *server) handleRegister(conn net.Conn, r *bufio.Reader, msg *proto.Msg) {
	sub := strings.ToLower(msg.Subdomain)
	if !subdomainRe.MatchString(sub) {
		proto.WriteMsg(conn, &proto.Msg{Type: proto.TypeError, Message: "invalid subdomain"}) //nolint:errcheck
		conn.Close()
		return
	}

	t := &Tunnel{subdomain: sub, ctrl: conn, pending: map[string]chan net.Conn{}}
	if err := s.registry.Add(t); err != nil {
		proto.WriteMsg(conn, &proto.Msg{Type: proto.TypeError, Message: err.Error()}) //nolint:errcheck
		conn.Close()
		return
	}
	defer func() {
		s.registry.Remove(t)
		t.close()
		log.Printf("tunnel down: %s.%s", sub, s.domain)
	}()

	domain := sub + "." + s.domain
	if err := proto.WriteMsg(conn, &proto.Msg{Type: proto.TypeRegistered, Domain: domain}); err != nil {
		return
	}
	log.Printf("tunnel up: https://%s -> %s", domain, conn.RemoteAddr())

	// 控制连接读循环:处理心跳,读错误即视为客户端下线。
	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second)) //nolint:errcheck
		m, err := proto.ReadMsg(r)
		if err != nil {
			return
		}
		if m.Type == proto.TypePing {
			if err := proto.WriteMsg(conn, &proto.Msg{Type: proto.TypePong}); err != nil {
				return
			}
		}
	}
}

func (s *server) handleData(conn net.Conn, _ *bufio.Reader, msg *proto.Msg) {
	t := s.registry.Get(strings.ToLower(msg.Subdomain))
	if t == nil || !t.deliver(msg.ConnID, conn) {
		conn.Close()
	}
	// 注意:此处 bufio.Reader 中不会有缓冲残留 —— 客户端在收到
	// 数据连接被接管前不会发送任何后续字节(先握手后透传)。
}

// ---- 公网 HTTP/HTTPS ----

func (s *server) publicHandler() http.Handler {
	suffix := "." + s.domain
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = pr.In.Host // 仅作占位,实际连接由 DialContext 决定
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		Transport: &http.Transport{
			// 核心:反代的“拨号”动作变成向隧道要一条数据连接。
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					host = addr
				}
				sub := strings.TrimSuffix(strings.ToLower(host), suffix)
				t := s.registry.Get(sub)
				if t == nil {
					return nil, fmt.Errorf("no tunnel for %q", host)
				}
				return t.OpenConn(ctx)
			},
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			log.Printf("proxy %s: %v", req.Host, err)
			http.Error(w, "通途: 目标隧道不在线或后端无响应", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host := req.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(host)
		if !strings.HasSuffix(host, suffix) || s.registry.Get(strings.TrimSuffix(host, suffix)) == nil {
			http.Error(w, "通途: 未找到该域名对应的隧道", http.StatusNotFound)
			return
		}
		proxy.ServeHTTP(w, req)
	})
}

func main() {
	var (
		controlAddr = flag.String("control", ":7000", "控制/数据端口监听地址")
		httpAddr    = flag.String("http", ":80", "公网 HTTP 监听地址")
		httpsAddr   = flag.String("https", ":443", "公网 HTTPS 监听地址(需配 -tls-cert/-tls-key)")
		domain      = flag.String("domain", "", "根域名,如 example.com(必填)")
		token       = flag.String("token", "", "客户端接入令牌,强烈建议设置")
		tlsCert     = flag.String("tls-cert", "", "泛域名证书 fullchain.pem 路径")
		tlsKey      = flag.String("tls-key", "", "泛域名证书私钥路径")
	)
	flag.Parse()
	if *domain == "" {
		log.Fatal("必须指定 -domain,例如 -domain example.com")
	}
	if *token == "" {
		log.Print("警告: 未设置 -token,任何人都可以注册隧道")
	}

	s := &server{domain: *domain, token: *token, registry: NewRegistry()}

	cl, err := net.Listen("tcp", *controlAddr)
	if err != nil {
		log.Fatalf("listen control: %v", err)
	}
	go s.serveControl(cl)
	log.Printf("控制端口: %s", *controlAddr)

	handler := s.publicHandler()

	if *tlsCert != "" && *tlsKey != "" {
		go func() {
			srv := &http.Server{Addr: *httpsAddr, Handler: handler, ReadHeaderTimeout: 15 * time.Second}
			log.Printf("HTTPS: %s (*.%s)", *httpsAddr, *domain)
			log.Fatal(srv.ListenAndServeTLS(*tlsCert, *tlsKey))
		}()
	} else {
		log.Print("未配置证书,HTTPS 未启用")
	}

	srv := &http.Server{Addr: *httpAddr, Handler: handler, ReadHeaderTimeout: 15 * time.Second}
	log.Printf("HTTP: %s", *httpAddr)
	log.Fatal(srv.ListenAndServe())
}
