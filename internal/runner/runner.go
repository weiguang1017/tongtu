// Package runner 把已启用的应用按凭证分组,同步到 Cloudflare
// (隧道 / ingress / DNS),然后为每个凭证托管一个 cloudflared 子进程。
//
// 隧道模型:每个凭证一条隧道(命名 tongtu-<凭证别名>),该凭证下所有
// 应用作为多条 ingress 规则共享这条隧道,只跑一个 cloudflared 进程。
package runner

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/config"
)

// appEntry 是一条待运行的应用及其解析结果。
// publish 为 false 表示应用已停用:不写 ingress 转发规则,但保留在组内
// (DNS 仍指向隧道),其域名的访问会落到兜底的宣传页,而不是无法访问。
type appEntry struct {
	name    string
	app     *config.App
	dom     *config.Domain
	publish bool
}

// group 是同一凭证下的全部应用,对应一条隧道、一个 cloudflared 进程。
type group struct {
	credName string
	token    string // CF API Token
	apps     []appEntry
}

// hostnames 返回组内实际发布(publish)的对外域名(按应用名有序)。
func (g *group) hostnames() []string {
	out := make([]string, 0, len(g.apps))
	for _, e := range g.apps {
		if e.publish {
			out = append(out, e.app.Hostname)
		}
	}
	return out
}

// Plan 描述一次 run 将要做什么,供 CLI 打印确认信息。
type Plan struct {
	groups []group
}

// Hostnames 返回本次计划实际发布的全部对外域名(有序)。
func (p *Plan) Hostnames() []string {
	var out []string
	for _, g := range p.groups {
		out = append(out, g.hostnames()...)
	}
	sort.Strings(out)
	return out
}

// Build 从配置挑选要运行的应用并按凭证分组。
// names 为空表示全部应用:已启用的发布转发规则,已停用的保留在组内
// (其域名兜底到宣传页);显式指定时忽略 enabled 标志(disabled 也可点名运行)。
func Build(cfg *config.Config, names []string) (*Plan, error) {
	selected := map[string]*config.App{}
	publish := map[string]bool{}
	if len(names) == 0 {
		enabled := 0
		for name, a := range cfg.Apps {
			selected[name], publish[name] = a, a.Enabled
			if a.Enabled {
				enabled++
			}
		}
		if enabled == 0 {
			return nil, fmt.Errorf("没有已启用的应用;先 tongtu app add 添加,或 tongtu app enable 启用")
		}
	} else {
		for _, name := range names {
			a, ok := cfg.Apps[name]
			if !ok {
				return nil, fmt.Errorf("应用 %q 不存在(tongtu app list 查看)", name)
			}
			selected[name], publish[name] = a, true
		}
	}
	groups, err := groupApps(cfg, selected, publish)
	if err != nil {
		return nil, err
	}
	return &Plan{groups: groups}, nil
}

// desiredGroups 计算运行中调和(Reconcile)的期望分组:已停用的应用仍留在
// 组内但不发布(域名兜底到宣传页,连接器不因全部停用而下线);sel 非空
// (点名启动)时进一步限定在点名集合内。与 Build 不同,允许结果为空 ——
// 全部应用删除是合法的期望状态(各组连接器随之停止,总开关保持打开)。
func desiredGroups(cfg *config.Config, sel []string) ([]group, error) {
	selSet := map[string]bool{}
	for _, n := range sel {
		selSet[n] = true
	}
	selected := map[string]*config.App{}
	publish := map[string]bool{}
	for name, a := range cfg.Apps {
		if len(sel) > 0 && !selSet[name] {
			continue
		}
		selected[name], publish[name] = a, a.Enabled
	}
	return groupApps(cfg, selected, publish)
}

// groupApps 把选中的应用按凭证分组并稳定排序。
func groupApps(cfg *config.Config, selected map[string]*config.App, publish map[string]bool) ([]group, error) {
	byCred := map[string]*group{}
	for name, a := range selected {
		credName, cred, dom, err := cfg.CredForApp(a)
		if err != nil {
			return nil, fmt.Errorf("应用 %s: %w", name, err)
		}
		g, ok := byCred[credName]
		if !ok {
			g = &group{credName: credName, token: cred.Token}
			byCred[credName] = g
		}
		g.apps = append(g.apps, appEntry{name: name, app: a, dom: dom, publish: publish[name]})
	}

	var out []group
	for _, credName := range config.SortedNames(byCred) {
		g := byCred[credName]
		sort.Slice(g.apps, func(i, j int) bool { return g.apps[i].name < g.apps[j].name })
		out = append(out, *g)
	}
	return out, nil
}

// Run 同步 Cloudflare 侧配置后启动 cloudflared,阻塞到 ctx 取消。
// CLI(tongtu run)入口;管理面板改用 Manager 以支持运行中动态调和,
// 这里薄封装保持 CLI 行为不变:要么全就绪要么不跑,Ctrl-C 优雅退出。
func Run(ctx context.Context, plan *Plan) error {
	m := NewManager()
	if err := m.Start(ctx, plan, nil); err != nil {
		return err
	}
	<-ctx.Done()
	m.Stop()
	return ctx.Err()
}

// syncResult 是 syncGroup 就绪后的关键标识,供进程托管与后续清理使用。
type syncResult struct {
	runToken  string // cloudflared 运行令牌
	accountID string
	tunnelID  string
}

// syncGroup 为一组应用就绪隧道、ingress 与 DNS。
// fallback 是 ingress 兜底服务(本机宣传页);已停用应用不写转发规则、
// 但保留 DNS,其域名的访问会落到兜底宣传页。
func syncGroup(ctx context.Context, g *group, fallback string) (syncResult, error) {
	client := cf.New(g.token)
	apiCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	accountID := g.apps[0].dom.AccountID

	// 1. 隧道:tongtu-<凭证别名>,查找或创建
	tunnelName := "tongtu-" + g.credName
	tunnel, err := client.FindTunnel(apiCtx, accountID, tunnelName)
	if err != nil {
		return syncResult{}, err
	}
	if tunnel == nil {
		if tunnel, err = client.CreateTunnel(apiCtx, accountID, tunnelName); err != nil {
			return syncResult{}, err
		}
		log.Printf("已创建隧道: %s", tunnelName)
	}

	// 2. ingress:每个已发布应用一条,全量覆盖;停用应用不写规则,落兜底
	rules := make([]cf.IngressRule, 0, len(g.apps))
	for _, e := range g.apps {
		if !e.publish {
			continue
		}
		r := cf.IngressRule{
			Hostname: e.app.Hostname,
			Service:  e.app.Proto + "://" + e.app.Local,
		}
		if e.app.NoTLSVerify || e.app.OriginServerName != "" {
			r.OriginRequest = &cf.OriginRequest{
				NoTLSVerify:      e.app.NoTLSVerify,
				OriginServerName: e.app.OriginServerName,
			}
		}
		rules = append(rules, r)
	}
	if err := client.SetIngress(apiCtx, accountID, tunnel.ID, rules, fallback); err != nil {
		return syncResult{}, err
	}

	// 3. DNS:每应用一条 CNAME(含停用应用 —— 域名保持解析,访问显示宣传页)
	for _, e := range g.apps {
		if err := client.UpsertTunnelCNAME(apiCtx, e.dom.ZoneID, e.app.Hostname, tunnel.ID); err != nil {
			return syncResult{}, err
		}
	}

	// 4. 隧道令牌
	tok, err := client.TunnelToken(apiCtx, accountID, tunnel.ID)
	if err != nil {
		return syncResult{}, err
	}
	return syncResult{runToken: tok, accountID: accountID, tunnelID: tunnel.ID}, nil
}

// clearIngress 清空隧道的 ingress 规则(只留 404 兜底),供组内应用全部
// 删除时收敛远端状态;隧道与 DNS 记录保留,重新添加可秒级恢复。
func clearIngress(ctx context.Context, token, accountID, tunnelID string) error {
	client := cf.New(token)
	apiCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return client.SetIngress(apiCtx, accountID, tunnelID, nil, "")
}

// RemoveAppDNS 删除某应用的 DNS CNAME(app rm 时调用;隧道保留给其余应用)。
func RemoveAppDNS(ctx context.Context, cfg *config.Config, app *config.App) error {
	_, cred, dom, err := cfg.CredForApp(app)
	if err != nil {
		return err
	}
	client := cf.New(cred.Token)
	apiCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return client.DeleteDNSRecord(apiCtx, dom.ZoneID, app.Hostname)
}
