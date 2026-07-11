package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"tongtu/internal/config"
	"tongtu/internal/runner"
)

func cmdApp(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fatalUsage("用法: tongtu app add|list|update|enable|disable|rm ...")
	}
	switch args[0] {
	case "add":
		return appAdd(ctx, args[1:])
	case "list":
		return appList(args[1:])
	case "update":
		return appUpdate(ctx, args[1:])
	case "enable":
		return appSetEnabled(args[1:], true)
	case "disable":
		return appSetEnabled(args[1:], false)
	case "rm":
		return appRm(ctx, args[1:])
	default:
		return fmt.Errorf("未知的 app 子命令 %q(可用: add list update enable disable rm)", args[0])
	}
}

// appFlags 定义 add/update 共用的参数集;update 时需区分"未传"与"传了空值",
// 故用 Visit 收集实际出现过的 flag。
type appFlags struct {
	fs               *flag.FlagSet
	hostname         *string
	local            *string
	proto            *string
	noTLSVerify      *bool
	originServerName *string
	disable          *bool
}

func newAppFlags(name string) *appFlags {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return &appFlags{
		fs:               fs,
		hostname:         fs.String("domain", "", "对外完整域名,如 blog.example.com(add 必填)"),
		local:            fs.String("local", "", "要转发到的服务地址 host:port,本机或内网可达地址均可,如 127.0.0.1:8080、192.168.1.10:5000(add 必填)"),
		proto:            fs.String("proto", "http", "本地服务协议: http / https / tcp"),
		noTLSVerify:      fs.Bool("no-tls-verify", false, "源站为自签 HTTPS 证书时跳过校验"),
		originServerName: fs.String("origin-server-name", "", "源站 TLS 握手使用的 SNI(源站证书域名与访问域名不一致时用)"),
		disable:          fs.Bool("disable", false, "添加后暂不启用"),
	}
}

func (f *appFlags) set() map[string]bool {
	seen := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { seen[fl.Name] = true })
	return seen
}

func validProto(p string) error {
	switch p {
	case "http", "https", "tcp":
		return nil
	}
	return fmt.Errorf("--proto 仅支持 http / https / tcp,收到 %q", p)
}

func appAdd(ctx context.Context, args []string) error {
	name, rest := splitName(args)
	f := newAppFlags("app add")
	f.fs.Parse(rest) //nolint:errcheck
	if name == "" || f.fs.NArg() != 0 {
		return fatalUsage("用法: tongtu app add <名称> --domain blog.example.com --local 127.0.0.1:8080 [--proto http] [--no-tls-verify] [--origin-server-name xxx] [--disable]")
	}
	if !config.NameRe.MatchString(name) {
		return fmt.Errorf("应用名 %q 不合法:仅允许小写字母、数字和中划线", name)
	}
	hostname := strings.ToLower(*f.hostname)
	if hostname == "" || *f.local == "" {
		return fatalUsage("--domain 与 --local 为必填")
	}
	if !config.HostnameRe.MatchString(hostname) {
		return fmt.Errorf("域名 %q 不合法", hostname)
	}
	if err := validProto(*f.proto); err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, exists := cfg.Apps[name]; exists {
		return fmt.Errorf("应用 %q 已存在;修改请用: tongtu app update %s ...", name, name)
	}
	// hostname 必须属于某个已登记域名
	if _, _, err := cfg.DomainForHostname(hostname); err != nil {
		return err
	}
	// hostname 不得与其他应用冲突
	for other, a := range cfg.Apps {
		if a.Hostname == hostname {
			return fmt.Errorf("域名 %s 已被应用 %s 使用", hostname, other)
		}
	}

	cfg.Apps[name] = &config.App{
		Hostname:         hostname,
		Local:            *f.local,
		Proto:            *f.proto,
		NoTLSVerify:      *f.noTLSVerify,
		OriginServerName: *f.originServerName,
		Enabled:          !*f.disable,
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	state := "已启用"
	if *f.disable {
		state = "未启用"
	}
	fmt.Printf("✓ 应用 %s 已添加(%s): https://%s -> %s://%s\n运行: tongtu run\n", name, state, hostname, *f.proto, *f.local)
	_ = ctx
	return nil
}

func appList(_ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Apps) == 0 {
		fmt.Println("(空)先执行: tongtu app add <名称> --domain <域名> --local <地址>")
		return nil
	}
	for _, name := range config.SortedNames(cfg.Apps) {
		a := cfg.Apps[name]
		state := "启用"
		if !a.Enabled {
			state = "停用"
		}
		extra := ""
		if a.NoTLSVerify {
			extra += " no-tls-verify"
		}
		if a.OriginServerName != "" {
			extra += " sni=" + a.OriginServerName
		}
		fmt.Printf("%-16s [%s] https://%s -> %s://%s%s\n", name, state, a.Hostname, a.Proto, a.Local, extra)
	}
	return nil
}

func appUpdate(ctx context.Context, args []string) error {
	name, rest := splitName(args)
	f := newAppFlags("app update")
	f.fs.Parse(rest) //nolint:errcheck
	if name == "" || f.fs.NArg() != 0 {
		return fatalUsage("用法: tongtu app update <名称> [--domain ...] [--local ...] [--proto ...] [--no-tls-verify] [--origin-server-name ...]")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	a, ok := cfg.Apps[name]
	if !ok {
		return fmt.Errorf("应用 %q 不存在(tongtu app list 查看)", name)
	}

	seen := f.set()
	if len(seen) == 0 {
		return fatalUsage("没有要更新的参数")
	}
	oldHostname := a.Hostname
	if seen["domain"] {
		hostname := strings.ToLower(*f.hostname)
		if !config.HostnameRe.MatchString(hostname) {
			return fmt.Errorf("域名 %q 不合法", hostname)
		}
		if _, _, err := cfg.DomainForHostname(hostname); err != nil {
			return err
		}
		for other, oa := range cfg.Apps {
			if other != name && oa.Hostname == hostname {
				return fmt.Errorf("域名 %s 已被应用 %s 使用", hostname, other)
			}
		}
		a.Hostname = hostname
	}
	if seen["local"] {
		a.Local = *f.local
	}
	if seen["proto"] {
		if err := validProto(*f.proto); err != nil {
			return err
		}
		a.Proto = *f.proto
	}
	if seen["no-tls-verify"] {
		a.NoTLSVerify = *f.noTLSVerify
	}
	if seen["origin-server-name"] {
		a.OriginServerName = *f.originServerName
	}
	if seen["disable"] {
		a.Enabled = !*f.disable
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if oldHostname != a.Hostname {
		// 换了对外域名:旧 hostname 的 CNAME 已无人使用,清掉避免悬空记录
		old := *a
		old.Hostname = oldHostname
		if err := runner.RemoveAppDNS(ctx, cfg, &old); err != nil {
			fmt.Printf("警告: 删除旧 DNS 记录 %s 失败: %v(可在 Cloudflare 控制台手动删除)\n", oldHostname, err)
		} else {
			fmt.Printf("已删除旧 DNS 记录: %s\n", oldHostname)
		}
	}
	fmt.Printf("✓ 应用 %s 已更新: https://%s -> %s://%s\n提示: 配置变更在下次 tongtu run 时生效;管理面板运行中修改则即时生效\n", name, a.Hostname, a.Proto, a.Local)
	return nil
}

func appSetEnabled(args []string, enabled bool) error {
	if len(args) != 1 {
		verb := "enable"
		if !enabled {
			verb = "disable"
		}
		return fatalUsage("用法: tongtu app " + verb + " <名称>")
	}
	name := strings.ToLower(args[0])
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	a, ok := cfg.Apps[name]
	if !ok {
		return fmt.Errorf("应用 %q 不存在", name)
	}
	a.Enabled = enabled
	if err := cfg.Save(); err != nil {
		return err
	}
	if enabled {
		fmt.Printf("✓ 应用 %s 已启用(下次 tongtu run 生效;管理面板运行中操作则即时生效)\n", name)
	} else {
		fmt.Printf("✓ 应用 %s 已停用(下次 tongtu run 不再包含它;管理面板运行中操作则即时下线)\n", name)
	}
	return nil
}

func appRm(ctx context.Context, args []string) error {
	name, rest := splitName(args)
	fs := flag.NewFlagSet("app rm", flag.ExitOnError)
	purgeDNS := fs.Bool("purge-dns", false, "同时删除 Cloudflare 上的 DNS 记录(默认保留,访问该域名显示通途介绍页)")
	fs.Parse(rest) //nolint:errcheck
	if name == "" || fs.NArg() != 0 {
		return fatalUsage("用法: tongtu app rm <名称> [--purge-dns]")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	a, ok := cfg.Apps[name]
	if !ok {
		return fmt.Errorf("应用 %q 不存在", name)
	}

	if *purgeDNS {
		if err := runner.RemoveAppDNS(ctx, cfg, a); err != nil {
			fmt.Printf("警告: 删除 DNS 记录 %s 失败: %v(可在 Cloudflare 控制台手动删除)\n", a.Hostname, err)
		} else {
			fmt.Printf("已删除 DNS 记录: %s\n", a.Hostname)
		}
	}
	delete(cfg.Apps, name)
	if err := cfg.Save(); err != nil {
		return err
	}
	if *purgeDNS {
		fmt.Printf("✓ 应用 %s 已删除\n提示: 隧道 ingress 在下次 tongtu run 时同步收敛\n", name)
	} else {
		fmt.Printf("✓ 应用 %s 已删除(DNS 保留,连接器运行期间访问 https://%s 将显示通途介绍页;彻底清理加 --purge-dns)\n", name, a.Hostname)
	}
	return nil
}
