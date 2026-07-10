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
type appEntry struct {
	name string
	app  *config.App
	dom  *config.Domain
}

// group 是同一凭证下的全部应用,对应一条隧道、一个 cloudflared 进程。
type group struct {
	credName string
	token    string // CF API Token
	apps     []appEntry
}

// hostnames 返回组内全部对外域名(按应用名有序,与 apps 一致)。
func (g *group) hostnames() []string {
	out := make([]string, 0, len(g.apps))
	for _, e := range g.apps {
		out = append(out, e.app.Hostname)
	}
	return out
}

// Plan 描述一次 run 将要做什么,供 CLI 打印确认信息。
type Plan struct {
	groups []group
}

// Hostnames 返回本次计划暴露的全部对外域名(有序)。
func (p *Plan) Hostnames() []string {
	var out []string
	for _, g := range p.groups {
		for _, e := range g.apps {
			out = append(out, e.app.Hostname)
		}
	}
	sort.Strings(out)
	return out
}

// Build 从配置挑选要运行的应用并按凭证分组。
// names 为空表示全部已启用应用;显式指定时忽略 enabled 标志(disabled 也可点名运行)。
func Build(cfg *config.Config, names []string) (*Plan, error) {
	selected := map[string]*config.App{}
	if len(names) == 0 {
		for name, a := range cfg.Apps {
			if a.Enabled {
				selected[name] = a
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("没有已启用的应用;先 tongtu app add 添加,或 tongtu app enable 启用")
		}
	} else {
		for _, name := range names {
			a, ok := cfg.Apps[name]
			if !ok {
				return nil, fmt.Errorf("应用 %q 不存在(tongtu app list 查看)", name)
			}
			selected[name] = a
		}
	}
	groups, err := groupApps(cfg, selected)
	if err != nil {
		return nil, err
	}
	return &Plan{groups: groups}, nil
}

// desiredGroups 计算运行中调和(Reconcile)的期望分组:应用需存在且已启用;
// sel 非空(点名启动)时进一步限定在点名集合内。与 Build 不同,允许结果为
// 空 —— 全部应用停用是合法的期望状态(各组连接器随之停止,总开关保持打开)。
func desiredGroups(cfg *config.Config, sel []string) ([]group, error) {
	selSet := map[string]bool{}
	for _, n := range sel {
		selSet[n] = true
	}
	selected := map[string]*config.App{}
	for name, a := range cfg.Apps {
		if !a.Enabled || (len(sel) > 0 && !selSet[name]) {
			continue
		}
		selected[name] = a
	}
	return groupApps(cfg, selected)
}

// groupApps 把选中的应用按凭证分组并稳定排序。
func groupApps(cfg *config.Config, selected map[string]*config.App) ([]group, error) {
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
		g.apps = append(g.apps, appEntry{name: name, app: a, dom: dom})
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
func syncGroup(ctx context.Context, g *group) (syncResult, error) {
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

	// 2. ingress:每应用一条,全量覆盖
	rules := make([]cf.IngressRule, 0, len(g.apps))
	for _, e := range g.apps {
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
	if err := client.SetIngress(apiCtx, accountID, tunnel.ID, rules); err != nil {
		return syncResult{}, err
	}

	// 3. DNS:每应用一条 CNAME
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
// 停用/删除时收敛远端状态;隧道与 DNS 记录保留,重新启用可秒级恢复。
func clearIngress(ctx context.Context, token, accountID, tunnelID string) error {
	client := cf.New(token)
	apiCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return client.SetIngress(apiCtx, accountID, tunnelID, nil)
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
