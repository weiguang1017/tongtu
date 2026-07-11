// tongtu 是「通途」客户端 —— 纯客户端内网穿透工具,公网侧完全由 Cloudflare 承担。
//
// 图形模式(推荐,双击或直接运行即可):
//
//	tongtu                                        # 启动图形管理界面并自动打开浏览器
//
// 子命令模式(配置持久化到 ~/.tongtu/config.json):
//
//	tongtu cred add mycf --token <CF_API_TOKEN>   # 保存凭证
//	tongtu domain add example.com                 # 登记域名
//	tongtu app add blog --domain blog.example.com --local 127.0.0.1:8080
//	tongtu run                                    # 运行全部已启用应用
//	tongtu web                                    # 图形管理界面(不自动开浏览器加 --open=false)
//
// 快捷模式(兼容旧用法,不落配置,临时暴露单个端口):
//
//	export TONGTU_CF_TOKEN=<Cloudflare API Token>
//	tongtu -domain example.com -name blog -local 127.0.0.1:8080
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/cli"
	"tongtu/internal/cloudflared"
)

var subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func main() {
	log.SetFlags(log.Ltime)

	// Ctrl-C / SIGTERM 触发优雅退出
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 无参数 = 图形模式:启动管理面板并自动打开浏览器
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"web", "--open"}
	}

	// 先尝试子命令;不匹配则回落到旧版单命令快捷路径
	handled, err := cli.Execute(ctx, args)
	if handled {
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "通途:", err)
			os.Exit(1)
		}
		return
	}

	quickMode(ctx)
}

// ---- 快捷模式(v2 兼容):tongtu -domain example.com -name blog -local ... ----

type quickApp struct {
	cfToken     string
	domain      string // 根域名,如 example.com
	name        string // 子域名,如 blog
	local       string // 本地服务地址 host:port
	scheme      string // 本地服务协议 http/https/tcp
	cleanup     bool   // 退出时删除隧道与 DNS 记录
	autoInstall bool   // 找不到 cloudflared 时自动下载
}

func quickMode(ctx context.Context) {
	var a quickApp
	flag.StringVar(&a.cfToken, "cf-token", "", "Cloudflare API Token(也可用环境变量 TONGTU_CF_TOKEN)")
	flag.StringVar(&a.domain, "domain", "", "根域名,如 example.com,需已托管在 Cloudflare(也可用环境变量 TONGTU_DOMAIN)")
	flag.StringVar(&a.name, "name", "", "子域名,如 blog(必填)")
	flag.StringVar(&a.local, "local", "127.0.0.1:8080", "要暴露的服务地址,本机或内网可达地址均可,如 127.0.0.1:8080、192.168.1.10:5000")
	flag.StringVar(&a.scheme, "proto", "http", "本地服务协议: http / https / tcp")
	flag.BoolVar(&a.cleanup, "cleanup", false, "退出时删除 Cloudflare 上的隧道与 DNS 记录(默认保留以便复用)")
	flag.BoolVar(&a.autoInstall, "auto-install", false, "未找到 cloudflared 时自动下载官方二进制到 ~/.tongtu/bin/")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "快捷模式用法: tongtu -domain example.com -name blog -local 127.0.0.1:8080\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\n多应用管理请用子命令,见: tongtu help\n")
	}
	flag.Parse()

	if a.cfToken == "" {
		a.cfToken = os.Getenv("TONGTU_CF_TOKEN")
	}
	if a.domain == "" {
		a.domain = os.Getenv("TONGTU_DOMAIN")
	}
	if err := a.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr)
		flag.Usage()
		os.Exit(2)
	}

	if err := a.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("通途: %v", err)
	}
	log.Print("通途已退出")
}

func (a *quickApp) validate() error {
	switch {
	case a.cfToken == "":
		return errors.New("缺少 Cloudflare API Token:用 -cf-token 或环境变量 TONGTU_CF_TOKEN 提供\n(Token 需具备 Account:Cloudflare Tunnel:Edit 与 Zone:DNS:Edit 权限,\n 申请入口: https://dash.cloudflare.com/profile/api-tokens)")
	case a.domain == "":
		return errors.New("缺少根域名:用 -domain 或环境变量 TONGTU_DOMAIN 提供,如 -domain example.com")
	case a.name == "":
		return errors.New("缺少子域名:用 -name 提供,如 -name blog(多应用管理请用子命令,见 tongtu help)")
	case !subdomainRe.MatchString(strings.ToLower(a.name)):
		return fmt.Errorf("子域名 %q 不合法:仅允许小写字母、数字和中划线", a.name)
	}
	switch a.scheme {
	case "http", "https", "tcp":
	default:
		return fmt.Errorf("-proto 仅支持 http / https / tcp,收到 %q", a.scheme)
	}
	return nil
}

// run 完成一次完整编排:CF 资源就绪 -> 托管 cloudflared 直到退出 -> 可选清理。
func (a *quickApp) run(ctx context.Context) error {
	a.name = strings.ToLower(a.name)
	a.domain = strings.ToLower(a.domain)
	fqdn := a.name + "." + a.domain

	// 1. 找 cloudflared(尽早失败,免得白建资源)
	bin, err := cloudflared.Find()
	if err != nil {
		if !a.autoInstall {
			return err
		}
		if bin, err = cloudflared.Install(ctx, ""); err != nil {
			return err
		}
	}

	client := cf.New(a.cfToken)
	apiCtx, cancelAPI := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelAPI()

	// 2. Zone -> account
	zone, err := client.FindZone(apiCtx, a.domain)
	if err != nil {
		return err
	}
	log.Printf("Cloudflare Zone 已确认: %s", zone.Name)

	// 3. 创建或复用隧道(命名 tongtu-<子域名>,便于在 CF 控制台辨认)
	tunnelName := "tongtu-" + a.name
	tunnel, err := client.FindTunnel(apiCtx, zone.Account.ID, tunnelName)
	if err != nil {
		return err
	}
	if tunnel == nil {
		if tunnel, err = client.CreateTunnel(apiCtx, zone.Account.ID, tunnelName); err != nil {
			return err
		}
		log.Printf("已创建隧道: %s", tunnelName)
	} else {
		log.Printf("复用已有隧道: %s", tunnelName)
	}

	// 4. 写 ingress: fqdn -> 本地服务(每次全量覆盖,保证与命令行一致)
	service := a.scheme + "://" + a.local
	if err := client.SetIngress(apiCtx, zone.Account.ID, tunnel.ID, []cf.IngressRule{
		{Hostname: fqdn, Service: service},
	}, ""); err != nil {
		return err
	}

	// 5. DNS CNAME: fqdn -> <tunnel_id>.cfargotunnel.com(代理开启,CF 自动签发 TLS)
	if err := client.UpsertTunnelCNAME(apiCtx, zone.ID, fqdn, tunnel.ID); err != nil {
		return err
	}

	// 6. 拿隧道令牌,托管 cloudflared
	token, err := client.TunnelToken(apiCtx, zone.Account.ID, tunnel.ID)
	if err != nil {
		return err
	}

	log.Printf("✓ 通途已就绪: https://%s -> %s", fqdn, a.local)
	runErr := cloudflared.Run(ctx, bin, token)

	// 7. 可选清理(用独立的 context —— 此时主 ctx 已被信号取消)
	if a.cleanup {
		a.doCleanup(client, zone, tunnel, fqdn)
	}
	return runErr
}

// doCleanup 删除 DNS 记录与隧道,失败只告警不中断退出流程。
func (a *quickApp) doCleanup(client *cf.Client, zone *cf.Zone, tunnel *cf.Tunnel, fqdn string) {
	log.Print("正在清理 Cloudflare 资源…")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := client.DeleteDNSRecord(ctx, zone.ID, fqdn); err != nil {
		log.Printf("删除 DNS 记录 %s 失败: %v", fqdn, err)
	} else {
		log.Printf("已删除 DNS 记录: %s", fqdn)
	}
	if err := client.DeleteTunnel(ctx, zone.Account.ID, tunnel.ID); err != nil {
		log.Printf("删除隧道失败: %v(cloudflared 连接可能尚未完全断开,可稍后在控制台删除)", err)
	} else {
		log.Printf("已删除隧道: tongtu-%s", a.name)
	}
}
