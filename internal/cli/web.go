package cli

import (
	"context"
	"errors"
	"flag"
	"log"

	"tongtu/internal/web"
)

func cmdWeb(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7080", "面板监听地址(默认仅本机)")
	token := fs.String("web-token", "", "面板访问令牌;监听非本机地址时必填(请求头 Authorization: Bearer <令牌>)")
	fs.Parse(args) //nolint:errcheck

	srv, err := web.New(*addr, *token)
	if err != nil {
		return err
	}
	err = srv.ListenAndServe(ctx)
	if errors.Is(err, context.Canceled) {
		log.Print("通途面板已退出")
		return nil
	}
	return err
}
