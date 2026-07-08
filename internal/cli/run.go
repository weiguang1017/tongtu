package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/cloudflared"
	"tongtu/internal/config"
	"tongtu/internal/runner"
)

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	autoInstall := fs.Bool("auto-install", false, "未找到 cloudflared 时自动下载官方二进制到 ~/.tongtu/bin/")
	fs.Parse(args) //nolint:errcheck

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	plan, err := runner.Build(cfg, fs.Args())
	if err != nil {
		return err
	}

	// cloudflared 缺失时按需安装(与 v2 快捷路径同语义)
	if _, err := cloudflared.Find(); err != nil {
		if !*autoInstall {
			return err
		}
		if _, err := cloudflared.Install(ctx); err != nil {
			return err
		}
	}

	err = runner.Run(ctx, plan)
	if errors.Is(err, context.Canceled) {
		log.Print("通途已退出")
		return nil
	}
	return err
}

func cmdStatus(ctx context.Context, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Apps) == 0 {
		fmt.Println("(空)没有任何应用")
		return nil
	}

	// 逐应用检查 DNS 记录是否指向所属凭证的隧道
	for _, name := range config.SortedNames(cfg.Apps) {
		a := cfg.Apps[name]
		state := "启用"
		if !a.Enabled {
			state = "停用"
		}
		credName, cred, dom, err := cfg.CredForApp(a)
		if err != nil {
			fmt.Printf("%-16s [%s] %s  配置错误: %v\n", name, state, a.Hostname, err)
			continue
		}
		client := cf.New(cred.Token)
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		dnsOK := "DNS未配置"
		tunnel, terr := client.FindTunnel(c, dom.AccountID, "tongtu-"+credName)
		if terr == nil && tunnel != nil {
			rec, rerr := client.FindDNSRecord(c, dom.ZoneID, a.Hostname)
			if rerr == nil && rec != nil && rec.Type == "CNAME" && rec.Content == tunnel.ID+".cfargotunnel.com" {
				dnsOK = "DNS就绪"
			}
		} else if terr == nil {
			dnsOK = "隧道未创建"
		} else {
			dnsOK = "查询失败: " + terr.Error()
		}
		cancel()
		fmt.Printf("%-16s [%s] https://%s -> %s://%s  %s\n", name, state, a.Hostname, a.Proto, a.Local, dnsOK)
	}
	fmt.Println("提示: 就绪状态在 tongtu run 时自动同步")
	return nil
}
