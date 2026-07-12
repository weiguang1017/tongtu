// Package buildinfo 暴露构建期注入的版本号,供管理面板与 CLI 展示。
package buildinfo

import "runtime/debug"

// Version 由构建脚本通过 -ldflags "-X tongtu/internal/buildinfo.Version=x.y.z" 注入
//(见 Makefile 与 .github/workflows/ci.yml)。直接 go run / go build 无 ldflags 时保持 "dev"。
var Version = "dev"

// String 返回适合展示的版本号。未注入时尝试从模块构建信息回退
//(如 go install module@vX.Y.Z),仍无则返回 "dev"。
func String() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
