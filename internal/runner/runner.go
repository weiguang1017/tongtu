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
	"sync"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/cloudflared"
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

	p := &Plan{}
	for _, credName := range config.SortedNames(byCred) {
		g := byCred[credName]
		sort.Slice(g.apps, func(i, j int) bool { return g.apps[i].name < g.apps[j].name })
		p.groups = append(p.groups, *g)
	}
	return p, nil
}

// Run 同步 Cloudflare 侧配置后启动 cloudflared,阻塞到 ctx 取消。
func Run(ctx context.Context, plan *Plan) error {
	bin, err := cloudflared.Find()
	if err != nil {
		return err
	}

	// 先把所有组的 CF 资源同步好(任何一组失败都不启动进程,保持"要么全就绪要么不跑")
	tokens := make([]string, len(plan.groups))
	for i := range plan.groups {
		g := &plan.groups[i]
		tok, err := syncGroup(ctx, g)
		if err != nil {
			return fmt.Errorf("凭证 %s: %w", g.credName, err)
		}
		tokens[i] = tok
	}

	for _, g := range plan.groups {
		for _, e := range g.apps {
			log.Printf("✓ 通途已就绪: https://%s -> %s://%s", e.app.Hostname, e.app.Proto, e.app.Local)
		}
	}

	// 每组一个 cloudflared,任一退出(ctx 取消)即整体收尾
	var wg sync.WaitGroup
	for i := range plan.groups {
		wg.Add(1)
		go func(g *group, token string) {
			defer wg.Done()
			if err := cloudflared.Run(ctx, bin, token); err != nil && ctx.Err() == nil {
				log.Printf("凭证 %s 的 cloudflared 异常结束: %v", g.credName, err)
			}
		}(&plan.groups[i], tokens[i])
	}
	wg.Wait()
	return ctx.Err()
}

// syncGroup 为一组应用就绪隧道、ingress 与 DNS,返回隧道运行令牌。
func syncGroup(ctx context.Context, g *group) (string, error) {
	client := cf.New(g.token)
	apiCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	accountID := g.apps[0].dom.AccountID

	// 1. 隧道:tongtu-<凭证别名>,查找或创建
	tunnelName := "tongtu-" + g.credName
	tunnel, err := client.FindTunnel(apiCtx, accountID, tunnelName)
	if err != nil {
		return "", err
	}
	if tunnel == nil {
		if tunnel, err = client.CreateTunnel(apiCtx, accountID, tunnelName); err != nil {
			return "", err
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
		return "", err
	}

	// 3. DNS:每应用一条 CNAME
	for _, e := range g.apps {
		if err := client.UpsertTunnelCNAME(apiCtx, e.dom.ZoneID, e.app.Hostname, tunnel.ID); err != nil {
			return "", err
		}
	}

	// 4. 隧道令牌
	return client.TunnelToken(apiCtx, accountID, tunnel.ID)
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
