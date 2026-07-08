package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"tongtu/internal/config"
)

func cmdCred(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fatalUsage("用法: tongtu cred add|list|update|rm ...")
	}
	switch args[0] {
	case "add":
		return credAdd(ctx, args[1:])
	case "list":
		return credList(args[1:])
	case "update":
		return credUpdate(ctx, args[1:])
	case "rm":
		return credRm(args[1:])
	default:
		return fmt.Errorf("未知的 cred 子命令 %q(可用: add list update rm)", args[0])
	}
}

func credAdd(ctx context.Context, args []string) error {
	alias, rest := splitName(args)
	fs := flag.NewFlagSet("cred add", flag.ExitOnError)
	token := fs.String("token", "", "Cloudflare API Token(必填;需 Account:Cloudflare Tunnel:Edit + Zone:DNS:Edit 权限)")
	fs.Parse(rest) //nolint:errcheck
	if alias == "" || fs.NArg() != 0 {
		return fatalUsage("用法: tongtu cred add <别名> --token <CF_API_TOKEN>")
	}
	if !config.NameRe.MatchString(alias) {
		return fmt.Errorf("别名 %q 不合法:仅允许小写字母、数字和中划线", alias)
	}
	if *token == "" {
		return fatalUsage("缺少 --token(申请入口: https://dash.cloudflare.com/profile/api-tokens)")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, exists := cfg.Creds[alias]; exists {
		return fmt.Errorf("凭证 %q 已存在;修改请用: tongtu cred update %s --token <新Token>", alias, alias)
	}

	if err := verifyToken(ctx, *token); err != nil {
		return fmt.Errorf("Token 验证失败: %w", err)
	}

	cfg.Creds[alias] = &config.Cred{Token: *token}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("✓ 凭证 %s 已保存(Token 已通过验证)\n", alias)
	return nil
}

func credList(_ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Creds) == 0 {
		fmt.Println("(空)先执行: tongtu cred add <别名> --token <CF_API_TOKEN>")
		return nil
	}
	for _, name := range config.SortedNames(cfg.Creds) {
		domains := cfg.DomainsUsingCred(name)
		fmt.Printf("%-16s %s  域名: %s\n", name, maskToken(cfg.Creds[name].Token), joinOr(domains, "-"))
	}
	return nil
}

func credUpdate(ctx context.Context, args []string) error {
	alias, rest := splitName(args)
	fs := flag.NewFlagSet("cred update", flag.ExitOnError)
	token := fs.String("token", "", "新的 Cloudflare API Token(必填)")
	fs.Parse(rest) //nolint:errcheck
	if alias == "" || fs.NArg() != 0 || *token == "" {
		return fatalUsage("用法: tongtu cred update <别名> --token <新Token>")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Creds[alias]; !ok {
		return fmt.Errorf("凭证 %q 不存在(tongtu cred list 查看)", alias)
	}
	if err := verifyToken(ctx, *token); err != nil {
		return fmt.Errorf("Token 验证失败: %w", err)
	}
	cfg.Creds[alias].Token = *token
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("✓ 凭证 %s 已更新\n", alias)
	return nil
}

func credRm(args []string) error {
	if len(args) != 1 {
		return fatalUsage("用法: tongtu cred rm <别名>")
	}
	alias := strings.ToLower(args[0])

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Creds[alias]; !ok {
		return fmt.Errorf("凭证 %q 不存在", alias)
	}
	if used := cfg.DomainsUsingCred(alias); len(used) > 0 {
		return fmt.Errorf("凭证 %s 正被域名引用: %s\n请先移除这些域名: tongtu domain rm <域名>", alias, strings.Join(used, ", "))
	}
	delete(cfg.Creds, alias)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("✓ 凭证 %s 已删除\n", alias)
	return nil
}

func joinOr(list []string, empty string) string {
	if len(list) == 0 {
		return empty
	}
	return strings.Join(list, ", ")
}
