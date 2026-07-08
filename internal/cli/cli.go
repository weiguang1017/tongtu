// Package cli 实现通途的子命令:cred / domain / zones / app / run / status / web。
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/config"
)

// Execute 分发子命令;返回 false 表示 args[0] 不是已知子命令(交回旧版单命令路径)。
func Execute(ctx context.Context, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "cred":
		return true, cmdCred(ctx, args[1:])
	case "domain":
		return true, cmdDomain(ctx, args[1:])
	case "zones":
		return true, cmdZones(ctx, args[1:])
	case "app":
		return true, cmdApp(ctx, args[1:])
	case "run":
		return true, cmdRun(ctx, args[1:])
	case "status":
		return true, cmdStatus(ctx, args[1:])
	case "web":
		return true, cmdWeb(ctx, args[1:])
	case "help", "-h", "--help":
		usage()
		return true, nil
	}
	return false, nil
}

func usage() {
	fmt.Print(`通途 tongtu —— 天堑变通途,家宽即服务器(纯客户端,公网侧由 Cloudflare 承担)

子命令:
  cred   add|list|update|rm     管理 Cloudflare API 凭证
  domain add|list|rm            管理已接入 Cloudflare 的根域名
  zones  [--cred 别名]          列出凭证可见的全部 Zone
  app    add|list|update|enable|disable|rm   管理对外应用
  run    [应用名...]            同步配置并运行(默认全部已启用应用)
  status                        查看各应用就绪状态
  web    [--addr 127.0.0.1:7080]  本地管理面板

快捷方式(不落配置,临时暴露):
  tongtu -domain example.com -name blog -local 127.0.0.1:8080

各子命令加 -h 查看参数,如: tongtu app add -h
`)
}

// loadConfig 读配置并给出统一错误。
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// requireCred 取凭证:显式指定别名,或配置里只有一个时自动选中。
func requireCred(cfg *config.Config, alias string) (string, *config.Cred, error) {
	if alias != "" {
		c, ok := cfg.Creds[alias]
		if !ok {
			return "", nil, fmt.Errorf("凭证 %q 不存在(tongtu cred list 查看)", alias)
		}
		return alias, c, nil
	}
	switch len(cfg.Creds) {
	case 0:
		return "", nil, fmt.Errorf("尚未添加任何凭证,请先执行: tongtu cred add <别名> --token <CF_API_TOKEN>")
	case 1:
		for name, c := range cfg.Creds {
			return name, c, nil
		}
	}
	return "", nil, fmt.Errorf("存在多个凭证,请用 --cred 指定(可选: %s)", strings.Join(config.SortedNames(cfg.Creds), ", "))
}

// apiCtx 派生带超时的 API 调用 context。
func apiCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Minute)
}

// maskToken 打码显示 Token,只留前 4 后 4。
func maskToken(t string) string {
	if len(t) <= 8 {
		return "****"
	}
	return t[:4] + strings.Repeat("*", len(t)-8) + t[len(t)-4:]
}

// verifyToken 用 CF API 校验 Token 有效性。
func verifyToken(ctx context.Context, token string) error {
	c, cancel := apiCtx(ctx)
	defer cancel()
	return cf.New(token).VerifyToken(c)
}

func fatalUsage(msg string) error {
	return fmt.Errorf("%s", msg)
}

// splitName 处理 "子命令 <名称> --flag ..." 形式:Go 的 flag 包遇到位置参数
// 即停止解析,因此先取出首个位置参数(名称),其余交给 FlagSet。
// 名称缺失或形如 flag 时返回空串。
func splitName(args []string) (name string, rest []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return strings.ToLower(args[0]), args[1:]
}

// die 供子命令统一输出错误(保持中文习惯)。
func die(err error) {
	fmt.Fprintln(os.Stderr, "通途:", err)
	os.Exit(1)
}
