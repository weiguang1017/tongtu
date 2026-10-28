// Package buildinfo 暴露应用版本号,供管理面板与 CLI 展示。
package buildinfo

import "runtime/debug"

// Version 默认为当前源码版本,构建脚本可通过
// -ldflags "-X tongtu/internal/buildinfo.Version=x.y.z" 覆盖
// (见 Makefile 与 .github/workflows/ci.yml)。
var Version = "1.1.0"

// String 返回适合展示的版本号。Version 为空或 dev 时尝试从模块构建信息回退
// (如 go install module@vX.Y.Z),仍无则返回 "dev"。
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
