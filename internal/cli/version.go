package cli

import (
	"context"
	"fmt"

	"tongtu/internal/buildinfo"
)

// cmdVersion 打印当前版本号(构建期由 -ldflags 注入,源码构建显示 dev)。
func cmdVersion(_ context.Context, _ []string) error {
	fmt.Printf("通途 tongtu %s\n", buildinfo.String())
	return nil
}
