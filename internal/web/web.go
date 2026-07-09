// Package web 提供通途的本地管理面板:REST API + 内嵌单页。
//
// 安全模型:默认只监听 127.0.0.1;监听非回环地址时必须提供访问令牌
// (请求头 Authorization: Bearer <token>)。面板进程内直接托管 cloudflared,
// 与 tongtu run 共用同一套 runner。
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/cloudflared"
	"tongtu/internal/config"
	"tongtu/internal/runner"
)

//go:embed static
var staticFS embed.FS

// Server 是管理面板服务。
type Server struct {
	addr  string
	token string // 非回环监听时必填
	open  bool   // 启动后自动打开系统浏览器
	ring  *logRing

	mu         sync.Mutex
	running    bool
	stop       context.CancelFunc
	runErr     error
	runApps    []string // 当前运行的应用名(空=全部启用)
	installing bool     // cloudflared 正在下载安装
	installErr string   // 上次自动安装失败原因(供前端展示)
}

// New 创建面板服务;addr 非回环且 token 为空时报错。
// openBrowser 为 true 时启动后自动用系统浏览器打开面板。
func New(addr, token string, openBrowser bool) (*Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("监听地址 %q 不合法: %w", addr, err)
	}
	if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && host != "localhost" && token == "" {
		return nil, fmt.Errorf("监听非本机地址 %s 时必须设置 --web-token,否则任何人都能改你的配置", addr)
	}
	return &Server{addr: addr, token: token, open: openBrowser, ring: newLogRing(2000)}, nil
}

// ListenAndServe 启动面板,阻塞到 ctx 取消。
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(static)))

	mux.HandleFunc("/api/config", s.auth(s.handleConfig))
	mux.HandleFunc("/api/creds", s.auth(s.handleCreds))
	mux.HandleFunc("/api/creds/", s.auth(s.handleCredItem))
	mux.HandleFunc("/api/domains", s.auth(s.handleDomains))
	mux.HandleFunc("/api/domains/", s.auth(s.handleDomainItem))
	mux.HandleFunc("/api/apps", s.auth(s.handleApps))
	mux.HandleFunc("/api/apps/", s.auth(s.handleAppItem))
	mux.HandleFunc("/api/zones", s.auth(s.handleZones))
	mux.HandleFunc("/api/cloudflared", s.auth(s.handleCloudflared))
	mux.HandleFunc("/api/cloudflared/install", s.auth(s.handleCloudflaredInstall))
	mux.HandleFunc("/api/settings", s.auth(s.handleSettings))
	mux.HandleFunc("/api/logs", s.auth(s.handleLogs))
	mux.HandleFunc("/api/run", s.auth(s.handleRun))
	mux.HandleFunc("/api/stop", s.auth(s.handleStop))
	mux.HandleFunc("/api/status", s.auth(s.handleStatus))

	// 面板日志与 cloudflared 子进程输出各 tee 一份进环形缓冲,供页面「运行日志」展示
	log.SetOutput(io.MultiWriter(os.Stderr, s.ring))
	cloudflared.TeeOutput(s.ring)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w(端口可能被占用,可用 --addr 换一个)", s.addr, err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		s.stopTunnels()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shCtx) //nolint:errcheck
	}()

	// 监听通配地址(0.0.0.0 / ::)时,浏览器打不开该地址,改用回环地址访问
	urlHost := ln.Addr().String()
	if h, p, err := net.SplitHostPort(urlHost); err == nil {
		if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() {
			urlHost = net.JoinHostPort("127.0.0.1", p)
		}
	}
	url := "http://" + urlHost + "/"
	log.Printf("通途管理面板: %s", url)
	if s.open {
		openBrowser(url)
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return ctx.Err()
}

// ---- 中间件与工具 ----

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
				jsonError(w, http.StatusUnauthorized, "无效的访问令牌")
				return
			}
		}
		h(w, r)
	}
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// maskToken 打码显示 Token。
func maskToken(t string) string {
	if len(t) <= 8 {
		return "****"
	}
	return t[:4] + strings.Repeat("*", len(t)-8) + t[len(t)-4:]
}

// ---- /api/config:全量只读视图(Token 打码) ----

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 GET")
		return
	}
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type credView struct {
		Name    string   `json:"name"`
		Token   string   `json:"token_masked"`
		Domains []string `json:"domains"`
	}
	type domainView struct {
		Name string   `json:"name"`
		Cred string   `json:"cred"`
		Apps []string `json:"apps"`
	}
	type appView struct {
		Name string `json:"name"`
		*config.App
	}
	out := struct {
		Creds   []credView   `json:"creds"`
		Domains []domainView `json:"domains"`
		Apps    []appView    `json:"apps"`
	}{[]credView{}, []domainView{}, []appView{}}
	for _, name := range config.SortedNames(cfg.Creds) {
		out.Creds = append(out.Creds, credView{name, maskToken(cfg.Creds[name].Token), cfg.DomainsUsingCred(name)})
	}
	for _, name := range config.SortedNames(cfg.Domains) {
		out.Domains = append(out.Domains, domainView{name, cfg.Domains[name].Cred, cfg.AppsUsingDomain(name)})
	}
	for _, name := range config.SortedNames(cfg.Apps) {
		out.Apps = append(out.Apps, appView{name, cfg.Apps[name]})
	}
	jsonOK(w, out)
}

// ---- /api/creds ----

func (s *Server) handleCreds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var in struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := decodeBody(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "请求体不合法: "+err.Error())
		return
	}
	in.Name = strings.ToLower(in.Name)
	if !config.NameRe.MatchString(in.Name) {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("别名 %q 不合法:只能用小写字母、数字和中划线(-),不能用下划线等其他符号,例如 tongtu-test", in.Name))
		return
	}
	if in.Token == "" {
		jsonError(w, http.StatusBadRequest, "请粘贴 Cloudflare API Token")
		return
	}
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, exists := cfg.Creds[in.Name]; exists {
		jsonError(w, http.StatusConflict, "凭证已存在,修改请用 PUT /api/creds/"+in.Name)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := cf.New(in.Token).VerifyToken(ctx); err != nil {
		jsonError(w, http.StatusBadRequest, "Token 验证失败: "+err.Error())
		return
	}
	cfg.Creds[in.Name] = &config.Cred{Token: in.Token}
	if err := cfg.Save(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"ok": in.Name})
}

func (s *Server) handleCredItem(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/creds/"))
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cred, ok := cfg.Creds[name]
	if !ok {
		jsonError(w, http.StatusNotFound, "凭证不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var in struct {
			Token string `json:"token"`
		}
		if err := decodeBody(r, &in); err != nil || in.Token == "" {
			jsonError(w, http.StatusBadRequest, "缺少 token")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		if err := cf.New(in.Token).VerifyToken(ctx); err != nil {
			jsonError(w, http.StatusBadRequest, "Token 验证失败: "+err.Error())
			return
		}
		cred.Token = in.Token
		if err := cfg.Save(); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]string{"ok": name})
	case http.MethodDelete:
		if used := cfg.DomainsUsingCred(name); len(used) > 0 {
			jsonError(w, http.StatusConflict, "凭证被域名引用: "+strings.Join(used, ", "))
			return
		}
		delete(cfg.Creds, name)
		if err := cfg.Save(); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]string{"ok": name})
	default:
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 PUT / DELETE")
	}
}

// ---- /api/domains ----

func (s *Server) handleDomains(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var in struct {
		Name string `json:"name"`
		Cred string `json:"cred"`
	}
	if err := decodeBody(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "请求体不合法: "+err.Error())
		return
	}
	in.Name = strings.ToLower(in.Name)
	if !config.HostnameRe.MatchString(in.Name) {
		jsonError(w, http.StatusBadRequest, "域名不合法")
		return
	}
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, exists := cfg.Domains[in.Name]; exists {
		jsonError(w, http.StatusConflict, "域名已登记")
		return
	}
	credName := in.Cred
	if credName == "" && len(cfg.Creds) == 1 {
		credName = config.SortedNames(cfg.Creds)[0]
	}
	cred, ok := cfg.Creds[credName]
	if !ok {
		jsonError(w, http.StatusBadRequest, "凭证不存在或未指定")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	zone, err := cf.New(cred.Token).FindZone(ctx, in.Name)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg.Domains[in.Name] = &config.Domain{Cred: credName, ZoneID: zone.ID, AccountID: zone.Account.ID}
	if err := cfg.Save(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"ok": in.Name})
}

func (s *Server) handleDomainItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 DELETE")
		return
	}
	name := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/domains/"))
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, ok := cfg.Domains[name]; !ok {
		jsonError(w, http.StatusNotFound, "域名未登记")
		return
	}
	if used := cfg.AppsUsingDomain(name); len(used) > 0 {
		jsonError(w, http.StatusConflict, "域名被应用引用: "+strings.Join(used, ", "))
		return
	}
	delete(cfg.Domains, name)
	if err := cfg.Save(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"ok": name})
}

// ---- /api/apps ----

// appInput 是新建/更新应用的请求体。
type appInput struct {
	Hostname         string `json:"hostname"`
	Local            string `json:"local"`
	Proto            string `json:"proto"`
	NoTLSVerify      bool   `json:"no_tls_verify"`
	OriginServerName string `json:"origin_server_name"`
	Enabled          *bool  `json:"enabled"`
}

func (in *appInput) validate(cfg *config.Config, self string) error {
	if !config.HostnameRe.MatchString(in.Hostname) {
		return errors.New("hostname 不合法")
	}
	if in.Local == "" {
		return errors.New("缺少 local")
	}
	switch in.Proto {
	case "http", "https", "tcp":
	default:
		return fmt.Errorf("proto 仅支持 http/https/tcp,收到 %q", in.Proto)
	}
	if _, _, err := cfg.DomainForHostname(in.Hostname); err != nil {
		return err
	}
	for other, a := range cfg.Apps {
		if other != self && a.Hostname == in.Hostname {
			return fmt.Errorf("域名 %s 已被应用 %s 使用", in.Hostname, other)
		}
	}
	return nil
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var in struct {
		Name string `json:"name"`
		appInput
	}
	if err := decodeBody(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "请求体不合法: "+err.Error())
		return
	}
	in.Name = strings.ToLower(in.Name)
	in.Hostname = strings.ToLower(in.Hostname)
	if in.Proto == "" {
		in.Proto = "http"
	}
	if !config.NameRe.MatchString(in.Name) {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("应用名 %q 不合法:只能用小写字母、数字和中划线(-),例如 my-blog", in.Name))
		return
	}
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, exists := cfg.Apps[in.Name]; exists {
		jsonError(w, http.StatusConflict, "应用已存在")
		return
	}
	if err := in.validate(cfg, ""); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	cfg.Apps[in.Name] = &config.App{
		Hostname: in.Hostname, Local: in.Local, Proto: in.Proto,
		NoTLSVerify: in.NoTLSVerify, OriginServerName: in.OriginServerName, Enabled: enabled,
	}
	if err := cfg.Save(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"ok": in.Name})
}

func (s *Server) handleAppItem(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/apps/"))
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a, ok := cfg.Apps[name]
	if !ok {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var in appInput
		if err := decodeBody(r, &in); err != nil {
			jsonError(w, http.StatusBadRequest, "请求体不合法: "+err.Error())
			return
		}
		in.Hostname = strings.ToLower(in.Hostname)
		if in.Proto == "" {
			in.Proto = a.Proto
		}
		if err := in.validate(cfg, name); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.Hostname, a.Local, a.Proto = in.Hostname, in.Local, in.Proto
		a.NoTLSVerify, a.OriginServerName = in.NoTLSVerify, in.OriginServerName
		if in.Enabled != nil {
			a.Enabled = *in.Enabled
		}
		if err := cfg.Save(); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]string{"ok": name})
	case http.MethodDelete:
		keepDNS := r.URL.Query().Get("keep_dns") == "1"
		if !keepDNS {
			ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
			defer cancel()
			if err := runner.RemoveAppDNS(ctx, cfg, a); err != nil {
				log.Printf("删除 DNS 记录 %s 失败: %v", a.Hostname, err)
			}
		}
		delete(cfg.Apps, name)
		if err := cfg.Save(); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]string{"ok": name})
	default:
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 PUT / DELETE")
	}
}

// ---- /api/run /api/stop /api/status:面板内托管 cloudflared ----

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var in struct {
		Apps []string `json:"apps"`
	}
	if r.ContentLength > 0 {
		if err := decodeBody(r, &in); err != nil {
			jsonError(w, http.StatusBadRequest, "请求体不合法: "+err.Error())
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	plan, err := runner.Build(cfg, in.Apps)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 提前探测 cloudflared,让前端能直接提示「一键安装」而不是启动后才失败
	if _, err := cloudflared.Find(); err != nil {
		jsonError(w, http.StatusPreconditionFailed, err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		jsonError(w, http.StatusConflict, "隧道已在运行,请先停止")
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.running, s.stop, s.runErr, s.runApps = true, cancel, nil, in.Apps
	go func() {
		err := runner.Run(runCtx, plan)
		s.mu.Lock()
		s.running, s.stop = false, nil
		if err != nil && !errors.Is(err, context.Canceled) {
			s.runErr = err
			log.Printf("隧道运行结束: %v", err)
		}
		s.mu.Unlock()
	}()
	jsonOK(w, map[string]any{"ok": true, "hostnames": plan.Hostnames()})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	s.stopTunnels()
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *Server) stopTunnels() {
	s.mu.Lock()
	if s.stop != nil {
		s.stop()
	}
	s.mu.Unlock()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 GET")
		return
	}
	s.mu.Lock()
	out := map[string]any{"running": s.running, "apps": s.runApps, "installing": s.installing}
	if s.runErr != nil {
		out["last_error"] = s.runErr.Error()
	}
	installErr := s.installErr
	s.mu.Unlock()
	if bin, err := cloudflared.Find(); err == nil {
		out["cloudflared"] = map[string]any{"found": true, "path": bin}
	} else {
		out["cloudflared"] = map[string]any{"found": false, "install_error": installErr}
	}
	jsonOK(w, out)
}

// ---- /api/zones:列出凭证可见的 Cloudflare Zone(供登记域名时选择) ----

func (s *Server) handleZones(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 GET")
		return
	}
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	credName := strings.ToLower(r.URL.Query().Get("cred"))
	if credName == "" && len(cfg.Creds) == 1 {
		credName = config.SortedNames(cfg.Creds)[0]
	}
	cred, ok := cfg.Creds[credName]
	if !ok {
		jsonError(w, http.StatusBadRequest, "凭证不存在或未指定(?cred=别名)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	zones, err := cf.New(cred.Token).ListZones(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	type zoneView struct {
		Name       string `json:"name"`
		Registered bool   `json:"registered"` // 是否已登记到本地配置
	}
	out := make([]zoneView, 0, len(zones))
	for _, z := range zones {
		_, exists := cfg.Domains[z.Name]
		out = append(out, zoneView{z.Name, exists})
	}
	jsonOK(w, out)
}

// ---- /api/cloudflared:探测与一键安装 ----

func (s *Server) handleCloudflared(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 GET")
		return
	}
	s.mu.Lock()
	installing := s.installing
	s.mu.Unlock()
	if bin, err := cloudflared.Find(); err == nil {
		jsonOK(w, map[string]any{"found": true, "path": bin, "installing": installing})
	} else {
		jsonOK(w, map[string]any{"found": false, "installing": installing, "hint": err.Error()})
	}
}

func (s *Server) handleCloudflaredInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	if bin, err := cloudflared.Find(); err == nil {
		jsonOK(w, map[string]any{"found": true, "path": bin})
		return
	}
	// 读取用户在设置里配的代理(可为空)
	proxy := ""
	if cfg, err := config.Load(); err == nil {
		proxy = cfg.Settings.Proxy
	}
	s.mu.Lock()
	if s.installing {
		s.mu.Unlock()
		jsonError(w, http.StatusConflict, "cloudflared 正在下载中,请稍候")
		return
	}
	s.installing = true
	s.installErr = ""
	s.mu.Unlock()

	// 下载放后台执行,不阻塞请求(也不受浏览器断开影响);前端轮询状态即可
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_, err := cloudflared.Install(ctx, proxy)
		s.mu.Lock()
		s.installing = false
		if err != nil {
			s.installErr = err.Error()
			log.Printf("自动安装 cloudflared 失败: %v", err)
		}
		s.mu.Unlock()
	}()
	jsonOK(w, map[string]any{"installing": true})
}

// ---- /api/settings:全局设置(目前仅下载代理) ----

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		jsonOK(w, map[string]any{"proxy": cfg.Settings.Proxy})
	case http.MethodPut:
		var in struct {
			Proxy string `json:"proxy"`
		}
		if err := decodeBody(r, &in); err != nil {
			jsonError(w, http.StatusBadRequest, "请求体不合法: "+err.Error())
			return
		}
		in.Proxy = strings.TrimSpace(in.Proxy)
		// 校验代理地址格式(允许留空表示不用代理)
		if in.Proxy != "" {
			u, perr := url.Parse(in.Proxy)
			if perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
				jsonError(w, http.StatusBadRequest, "代理地址需形如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080")
				return
			}
		}
		cfg.Settings.Proxy = in.Proxy
		if err := cfg.Save(); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]any{"proxy": in.Proxy})
	default:
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 GET / PUT")
	}
}

// ---- /api/logs:增量拉取运行日志 ----

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "仅支持 GET")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	next, lines := s.ring.since(after)
	jsonOK(w, map[string]any{"next": next, "lines": lines})
}
