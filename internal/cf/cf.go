// Package cf 是 Cloudflare REST API 的极简客户端,只覆盖通途需要的几个接口:
// 查询 Zone、创建/查询/删除 Cloudflare Tunnel、写隧道 ingress 配置、
// 管理 DNS CNAME 记录、获取隧道运行令牌。
//
// 纯标准库实现(net/http + encoding/json),不引入任何 SDK。
package cf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

// Client 持有 API Token 的 Cloudflare API 客户端。
type Client struct {
	token string
	http  *http.Client
}

func New(token string) *Client {
	return &Client{
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// envelope 是 Cloudflare API 的统一响应外壳。
type envelope struct {
	Success bool            `json:"success"`
	Errors  []apiError      `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// do 发起请求并把 result 解码到 out(out 可为 nil)。
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Cloudflare API: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("读取 Cloudflare API 响应: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("解析 Cloudflare API 响应 (HTTP %d): %w", resp.StatusCode, err)
	}
	if !env.Success {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("Cloudflare API 鉴权失败 (HTTP %d): %s\n请检查 API Token 是否有效,且具备 Account:Cloudflare Tunnel:Edit 与 Zone:DNS:Edit 权限(用户令牌 cfut_ 与账户令牌 cfat_ 均支持)", resp.StatusCode, joinErrors(env.Errors))
		}
		return fmt.Errorf("Cloudflare API 错误 (HTTP %d): %s", resp.StatusCode, joinErrors(env.Errors))
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("解析 result: %w", err)
		}
	}
	return nil
}

func joinErrors(errs []apiError) string {
	if len(errs) == 0 {
		return "未知错误"
	}
	s := ""
	for i, e := range errs {
		if i > 0 {
			s += "; "
		}
		s += fmt.Sprintf("[%d] %s", e.Code, e.Message)
	}
	return s
}

// VerifyToken 校验 API Token 是否有效。
//
// Cloudflare 有两类令牌:用户令牌(cfut_ 前缀,在 /profile/api-tokens 创建)与
// 账户令牌(cfat_ 前缀,在带账户 ID 的 URL 下创建)。二者校验端点不同 ——
// 账户令牌打 /user/tokens/verify 会返回 401,即使令牌完全有效。因此这里先试用户
// 端点(能拿到 active 状态),失败再回退到一次通用鉴权调用(列账户)确认有效性,
// 从而对两类令牌都适用。后续建隧道 / 写 DNS 的调用都走 /accounts 与 /zones,两类
// 令牌一致,无需区分。
func (c *Client) VerifyToken(ctx context.Context) error {
	var res struct {
		Status string `json:"status"`
	}
	userErr := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, &res)
	if userErr == nil {
		if res.Status != "active" {
			return fmt.Errorf("API Token 状态为 %q(非 active),请在 Cloudflare 控制台检查", res.Status)
		}
		return nil
	}

	// 用户端点失败:可能是账户令牌(cfat_)。用一次通用鉴权调用确认令牌本身有效。
	var accounts []struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, "/accounts?per_page=1", nil, &accounts); err != nil {
		// 两个端点都失败 —— 令牌确实无效,返回信息更具体的原始错误。
		return userErr
	}
	// 能通过鉴权列出账户 = 令牌有效(账户令牌的正常路径)。
	return nil
}

// ---- Zone ----

// Zone 是域名在 Cloudflare 的托管单元。
type Zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account struct {
		ID string `json:"id"`
	} `json:"account"`
}

// FindZone 按根域名精确查找 Zone(域名必须已托管在 Cloudflare)。
func (c *Client) FindZone(ctx context.Context, domain string) (*Zone, error) {
	var zones []Zone
	q := url.Values{"name": {domain}, "status": {"active"}}
	if err := c.do(ctx, http.MethodGet, "/zones?"+q.Encode(), nil, &zones); err != nil {
		return nil, err
	}
	if len(zones) == 0 {
		return nil, fmt.Errorf("域名 %s 未托管在此 Cloudflare 账号下(或 Token 无该 Zone 权限)", domain)
	}
	return &zones[0], nil
}

// ListZones 列出 Token 可见的全部 active Zone(单页 50 个,通途场景足够)。
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	q := url.Values{"status": {"active"}, "per_page": {"50"}}
	if err := c.do(ctx, http.MethodGet, "/zones?"+q.Encode(), nil, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

// ---- Cloudflare Tunnel ----

// Tunnel 是一条 Cloudflare Tunnel。
type Tunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FindTunnel 按名称查找未删除的隧道,不存在返回 nil。
func (c *Client) FindTunnel(ctx context.Context, accountID, name string) (*Tunnel, error) {
	var tunnels []Tunnel
	q := url.Values{"name": {name}, "is_deleted": {"false"}}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel?%s", accountID, q.Encode())
	if err := c.do(ctx, http.MethodGet, path, nil, &tunnels); err != nil {
		return nil, err
	}
	for i := range tunnels {
		if tunnels[i].Name == name {
			return &tunnels[i], nil
		}
	}
	return nil, nil
}

// CreateTunnel 创建一条远程配置(config_src=cloudflare)的隧道。
func (c *Client) CreateTunnel(ctx context.Context, accountID, name string) (*Tunnel, error) {
	var t Tunnel
	body := map[string]string{"name": name, "config_src": "cloudflare"}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel", accountID)
	if err := c.do(ctx, http.MethodPost, path, body, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteTunnel 删除隧道(需先断开连接,cloudflared 退出后调用)。
func (c *Client) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s", accountID, tunnelID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// TunnelToken 获取 cloudflared 运行该隧道所需的令牌。
func (c *Client) TunnelToken(ctx context.Context, accountID, tunnelID string) (string, error) {
	var token string
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/token", accountID, tunnelID)
	if err := c.do(ctx, http.MethodGet, path, nil, &token); err != nil {
		return "", err
	}
	return token, nil
}

// IngressRule 是隧道 ingress 配置里的一条转发规则。
type IngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
}

// OriginRequest 是连接源站(本地服务)时的 TLS 等参数。
// 访客侧 HTTPS 证书由 Cloudflare 边缘自动签发,与此无关。
type OriginRequest struct {
	NoTLSVerify      bool   `json:"noTLSVerify,omitempty"`      // 源站为自签证书时跳过校验
	OriginServerName string `json:"originServerName,omitempty"` // 源站 TLS 握手 SNI
}

// SetIngress 全量覆盖写入隧道的 ingress 规则(远程配置模式)。
// 最后一条必须是无 hostname 的兜底规则,调用方无需自带,这里自动补上 404。
func (c *Client) SetIngress(ctx context.Context, accountID, tunnelID string, rules []IngressRule) error {
	rules = append(rules, IngressRule{Service: "http_status:404"})
	body := map[string]any{"config": map[string]any{"ingress": rules}}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountID, tunnelID)
	return c.do(ctx, http.MethodPut, path, body, nil)
}

// ---- DNS ----

// DNSRecord 是一条 DNS 记录(只用到 CNAME)。
type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
}

// FindDNSRecord 按完整域名查找记录(任意类型),不存在返回 nil。
func (c *Client) FindDNSRecord(ctx context.Context, zoneID, fqdn string) (*DNSRecord, error) {
	var recs []DNSRecord
	q := url.Values{"name": {fqdn}}
	path := fmt.Sprintf("/zones/%s/dns_records?%s", zoneID, q.Encode())
	if err := c.do(ctx, http.MethodGet, path, nil, &recs); err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return &recs[0], nil
}

// UpsertTunnelCNAME 创建或更新 fqdn -> <tunnelID>.cfargotunnel.com 的代理 CNAME。
// 若已存在指向其他目标的非通途记录,报错让用户自行处理,避免误覆盖。
func (c *Client) UpsertTunnelCNAME(ctx context.Context, zoneID, fqdn, tunnelID string) error {
	target := tunnelID + ".cfargotunnel.com"
	want := DNSRecord{
		Type: "CNAME", Name: fqdn, Content: target,
		Proxied: true, Comment: "由通途 tongtu 管理",
	}

	existing, err := c.FindDNSRecord(ctx, zoneID, fqdn)
	if err != nil {
		return err
	}
	switch {
	case existing == nil:
		path := fmt.Sprintf("/zones/%s/dns_records", zoneID)
		return c.do(ctx, http.MethodPost, path, want, nil)
	case existing.Type == "CNAME" && existing.Content == target:
		return nil // 已就位
	case existing.Type == "CNAME" && isTunnelTarget(existing.Content):
		// 指向别的通途/CF 隧道,更新到当前隧道
		path := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, existing.ID)
		return c.do(ctx, http.MethodPut, path, want, nil)
	default:
		return fmt.Errorf("DNS 记录 %s 已存在(%s -> %s),且不是隧道记录,拒绝覆盖;请先在 Cloudflare 控制台处理", fqdn, existing.Type, existing.Content)
	}
}

// DeleteDNSRecord 删除 fqdn 对应的隧道 CNAME(仅当它确实指向 CF 隧道时)。
func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, fqdn string) error {
	existing, err := c.FindDNSRecord(ctx, zoneID, fqdn)
	if err != nil {
		return err
	}
	if existing == nil || existing.Type != "CNAME" || !isTunnelTarget(existing.Content) {
		return nil
	}
	path := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, existing.ID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func isTunnelTarget(content string) bool {
	const suffix = ".cfargotunnel.com"
	return len(content) > len(suffix) && content[len(content)-len(suffix):] == suffix
}
