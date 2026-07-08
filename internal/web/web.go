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
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/config"
	"tongtu/internal/runner"
)

//go:embed static
var staticFS embed.FS

// Server 是管理面板服务。
type Server struct {
	addr  string
	token string // 非回环监听时必填

	mu      sync.Mutex
	running bool
	stop    context.CancelFunc
	runErr  error
	runApps []string // 当前运行的应用名(空=全部启用)
}

// New 创建面板服务;addr 非回环且 token 为空时报错。
func New(addr, token string) (*Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("监听地址 %q 不合法: %w", addr, err)
	}
	if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && host != "localhost" && token == "" {
		return nil, fmt.Errorf("监听非本机地址 %s 时必须设置 --web-token,否则任何人都能改你的配置", addr)
	}
	return &Server{addr: addr, token: token}, nil
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
	mux.HandleFunc("/api/run", s.auth(s.handleRun))
	mux.HandleFunc("/api/stop", s.auth(s.handleStop))
	mux.HandleFunc("/api/status", s.auth(s.handleStatus))

	srv := &http.Server{Addr: s.addr, Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		s.stopTunnels()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shCtx) //nolint:errcheck
	}()

	log.Printf("通途管理面板: http://%s/", s.addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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
	if !config.NameRe.MatchString(in.Name) || in.Token == "" {
		jsonError(w, http.StatusBadRequest, "name 不合法或缺少 token")
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
		jsonError(w, http.StatusBadRequest, "name 不合法")
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
	out := map[string]any{"running": s.running, "apps": s.runApps}
	if s.runErr != nil {
		out["last_error"] = s.runErr.Error()
	}
	s.mu.Unlock()
	jsonOK(w, out)
}
