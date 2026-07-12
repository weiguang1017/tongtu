//go:build !desktop

package cli

import (
	"context"
	"errors"
)

var errNoDesktop = errors.New("本构建不含桌面组件(headless 版);请使用 tongtu web,或下载桌面版安装包")

func cmdDesktop(context.Context, []string) error { return errNoDesktop }
func cmdWindow(context.Context, []string) error  { return errNoDesktop }
