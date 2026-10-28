package cloudflared

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Security baselines for the October 2026 eMudhra certificate compromise:
// https://blog.cloudflare.com/emudhra-certificate-compromise/
const (
	MinimumSecureVersion    = "2026.10.2"
	MinimumSecureMacVersion = "2026.10.3"
)

var versionPattern = regexp.MustCompile(`(?m)^cloudflared version ([0-9]+\.[0-9]+\.[0-9]+)(?:\s|$)`)

// BinaryInfo describes the selected connector. Found means safe to start;
// Path is also present when an installed connector needs an upgrade.
type BinaryInfo struct {
	Found          bool   `json:"found"`
	Path           string `json:"path,omitempty"`
	Version        string `json:"version,omitempty"`
	MinimumVersion string `json:"minimum_version"`
	Hint           string `json:"hint,omitempty"`
}

func minimumVersion() string {
	// Use the standalone macOS baseline for all macOS installations. This also
	// covers managed downloads and copies whose package provenance is unknown.
	if runtime.GOOS == "darwin" {
		return MinimumSecureMacVersion
	}
	return MinimumSecureVersion
}

// Inspect prefers PATH when it is safe, then tries tongtu's managed download.
// Thus an obsolete PATH binary cannot mask a newly installed safe connector.
func Inspect() BinaryInfo {
	var paths []string
	if p, err := exec.LookPath("cloudflared"); err == nil {
		paths = append(paths, p)
	}
	managed := filepath.Join(binDir(), exeName())
	if stat, err := os.Stat(managed); err == nil && !stat.IsDir() {
		paths = append(paths, managed)
	}
	return inspectPaths(paths)
}

func inspectPaths(paths []string) BinaryInfo {
	var rejected BinaryInfo
	for _, path := range paths {
		info, err := checkBinary(path)
		if err == nil {
			return info
		}
		if rejected.Path == "" {
			info.Hint = err.Error()
			rejected = info
		}
	}
	if rejected.Path != "" {
		return rejected
	}
	return BinaryInfo{MinimumVersion: minimumVersion(), Hint: `未找到 cloudflared，请在概览页一键安装，或运行 tongtu run --auto-install。
macOS 也可执行 brew install cloudflared；已安装旧版本请执行 brew update && brew upgrade cloudflared。`}
}

func checkBinary(bin string) (BinaryInfo, error) {
	info := BinaryInfo{Path: bin, MinimumVersion: minimumVersion()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return info, fmt.Errorf("无法读取 cloudflared 版本(%s): %w", bin, err)
	}
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(string(out)))
	if len(match) != 2 {
		return info, fmt.Errorf("无法识别 cloudflared 正式版本(%s)，请安装官方安全版本 >= %s", bin, info.MinimumVersion)
	}
	info.Version = match[1]
	if compareVersions(info.Version, info.MinimumVersion) < 0 {
		return info, fmt.Errorf("cloudflared %s 存在已知证书信任风险，要求 >= %s。请在概览页一键升级，或使用 --auto-install；Homebrew 用户可执行 brew update && brew upgrade cloudflared", info.Version, info.MinimumVersion)
	}
	info.Found = true
	return info, nil
}

// Inputs are validated numeric release versions (year.month.patch).
func compareVersions(a, b string) int {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.ParseUint(aa[i], 10, 64)
		y, _ := strconv.ParseUint(bb[i], 10, 64)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
