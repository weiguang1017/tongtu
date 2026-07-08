package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"tongtu/internal/cf"
	"tongtu/internal/config"
)

func cmdDomain(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fatalUsage("用法: tongtu domain add|list|rm ...")
	}
	switch args[0] {
	case "add":
		return domainAdd(ctx, args[1:])
	case "list":
		return domainList(args[1:])
	case "rm":
		return domainRm(args[1:])
	default:
		return fmt.Errorf("未知的 domain 子命令 %q(可用: add list rm)", args[0])
	}
}

func domainAdd(ctx context.Context, args []string) error {
	domain, rest := splitName(args)
	fs := flag.NewFlagSet("domain add", flag.ExitOnError)
	credAlias := fs.String("cred", "", "使用的凭证别名(只有一个凭证时可省略)")
	fs.Parse(rest) //nolint:errcheck
	if domain == "" || fs.NArg() != 0 {
		return fatalUsage("用法: tongtu domain add <根域名> [--cred 别名]")
	}
	if !config.HostnameRe.MatchString(domain) {
		return fmt.Errorf("域名 %q 不合法", domain)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, exists := cfg.Domains[domain]; exists {
		return fmt.Errorf("域名 %s 已登记(tongtu domain list 查看)", domain)
	}
	credName, cred, err := requireCred(cfg, *credAlias)
	if err != nil {
		return err
	}

	// 在线验证:zone 存在且 Token 有权限,顺便缓存 zone_id / account_id
	c, cancel := apiCtx(ctx)
	defer cancel()
	zone, err := cf.New(cred.Token).FindZone(c, domain)
	if err != nil {
		return err
	}

	cfg.Domains[domain] = &config.Domain{
		Cred:      credName,
		ZoneID:    zone.ID,
		AccountID: zone.Account.ID,
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("✓ 域名 %s 已登记(凭证: %s,Zone 已验证)\n", domain, credName)
	return nil
}

func domainList(_ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Domains) == 0 {
		fmt.Println("(空)先执行: tongtu domain add <根域名>")
		return nil
	}
	for _, name := range config.SortedNames(cfg.Domains) {
		d := cfg.Domains[name]
		apps := cfg.AppsUsingDomain(name)
		fmt.Printf("%-24s 凭证: %-12s 应用: %s\n", name, d.Cred, joinOr(apps, "-"))
	}
	return nil
}

func domainRm(args []string) error {
	if len(args) != 1 {
		return fatalUsage("用法: tongtu domain rm <根域名>")
	}
	domain := strings.ToLower(args[0])

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Domains[domain]; !ok {
		return fmt.Errorf("域名 %s 未登记", domain)
	}
	if used := cfg.AppsUsingDomain(domain); len(used) > 0 {
		return fmt.Errorf("域名 %s 正被应用引用: %s\n请先删除这些应用: tongtu app rm <名称>", domain, strings.Join(used, ", "))
	}
	delete(cfg.Domains, domain)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("✓ 域名 %s 已移除登记(Cloudflare 上的 Zone 不受影响)\n", domain)
	return nil
}

func cmdZones(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("zones", flag.ExitOnError)
	credAlias := fs.String("cred", "", "使用的凭证别名(只有一个凭证时可省略)")
	fs.Parse(args) //nolint:errcheck

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	credName, cred, err := requireCred(cfg, *credAlias)
	if err != nil {
		return err
	}

	c, cancel := apiCtx(ctx)
	defer cancel()
	zones, err := cf.New(cred.Token).ListZones(c)
	if err != nil {
		return err
	}
	if len(zones) == 0 {
		fmt.Printf("凭证 %s 看不到任何 active Zone;请确认 Token 的 Zone 权限范围\n", credName)
		return nil
	}
	fmt.Printf("凭证 %s 可用的域名:\n", credName)
	for _, z := range zones {
		mark := " "
		if _, ok := cfg.Domains[z.Name]; ok {
			mark = "*" // 已登记
		}
		fmt.Printf(" %s %s\n", mark, z.Name)
	}
	fmt.Println("(* 表示已登记;登记用: tongtu domain add <域名>)")
	return nil
}
