package cli

import (
	"context"
	"fmt"

	"tongtu/internal/buildinfo"
)

// cmdVersion 打印当前版本号(默认采用源码版本,构建期可由 -ldflags 覆盖)。
func cmdVersion(_ context.Context, _ []string) error {
	fmt.Printf("通途 tongtu %s\n", buildinfo.String())
	return nil
}
