package cloudflared

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2026.9.10", MinimumSecureVersion, -1},
		{"2026.10.1", MinimumSecureVersion, -1},
		{MinimumSecureVersion, MinimumSecureVersion, 0},
		{"2026.10.4", MinimumSecureVersion, 1},
		{"2027.1.0", MinimumSecureVersion, 1},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func fakeBinary(t *testing.T, path, output string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\nprintf '%s\\n' '" + output + "'\nexit 0\nfi\nexit 99\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityGate(t *testing.T) {
	for _, tt := range []struct {
		output string
		secure bool
	}{
		{"cloudflared version 2026.7.1 (built 2026-07-09-13:15 UTC)", false},
		{"cloudflared version 2026.10.2", runtime.GOOS != "darwin"},
		{"cloudflared version 2026.10.3", true},
		{"cloudflared version 2026.10.5 (built 2026-10-27T00:00:00Z)", true},
		{"cloudflared version 2027.1.0", true},
		{"cloudflared version 2026.10.3-rc1", false},
		{"cloudflared version DEV", false},
		{"another tool version 2027.1.0", false},
	} {
		t.Run(tt.output, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "cloudflared")
			fakeBinary(t, bin, tt.output)
			info, err := checkBinary(bin)
			if info.Found != tt.secure || (err == nil) != tt.secure {
				t.Fatalf("checkBinary = %+v, %v; secure=%v", info, err, tt.secure)
			}
			if !tt.secure {
				// An explicit path must be rejected before the tunnel subprocess
				// runs, instead of entering the retry loop (exit 99 in the fixture).
				if err := Run(context.Background(), bin, "unused"); err == nil || strings.Contains(err.Error(), "99") {
					t.Fatalf("Run did not reject unsafe binary before starting: %v", err)
				}
			}
		})
	}
}

func TestOldPATHDoesNotMaskUpgradedManagedBinary(t *testing.T) {
	dir := t.TempDir()
	old, updated := filepath.Join(dir, "old"), filepath.Join(dir, "updated")
	fakeBinary(t, old, "cloudflared version 2026.5.2")
	fakeBinary(t, updated, "cloudflared version 2026.10.5")
	info := inspectPaths([]string{old, updated})
	if !info.Found || info.Path != updated {
		t.Fatalf("managed upgrade not selected: %+v", info)
	}
	info = inspectPaths([]string{old})
	if info.Found || info.Version != "2026.5.2" || info.Hint == "" {
		t.Fatalf("old installation must expose an upgrade hint: %+v", info)
	}
}

func TestInstallValidatesBeforeReplacing(t *testing.T) {
	for _, tt := range []struct {
		name, output string
		status       int
		wantSuccess  bool
	}{
		{"secure", "cloudflared version 2026.10.5", http.StatusOK, true},
		{"old", "cloudflared version 2026.7.1", http.StatusOK, false},
		{"invalid", "invalid download", http.StatusOK, false},
		{"http-error", "", http.StatusBadGateway, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			dst, fixture := filepath.Join(dir, "installed"), filepath.Join(dir, "fixture")
			fakeBinary(t, dst, "cloudflared version 2026.10.3")
			original, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			fakeBinary(t, fixture, tt.output)
			payload, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS == "darwin" {
				var buf bytes.Buffer
				gz := gzip.NewWriter(&buf)
				tw := tar.NewWriter(gz)
				if err := tw.WriteHeader(&tar.Header{Name: "cloudflared", Mode: 0o755, Size: int64(len(payload))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := tw.Close(); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				payload = buf.Bytes()
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				w.Write(payload)
			}))
			defer srv.Close()
			_, err = installTo(context.Background(), "", srv.URL, dst)
			if (err == nil) != tt.wantSuccess {
				t.Fatalf("installTo error=%v; wantSuccess=%v", err, tt.wantSuccess)
			}
			current, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.wantSuccess && !bytes.Equal(original, current) {
				t.Fatal("failed upgrade changed installed binary")
			}
			if tt.wantSuccess {
				info, err := checkBinary(dst)
				if err != nil || info.Version != "2026.10.5" {
					t.Fatalf("installed binary = %+v, %v", info, err)
				}
			}
			entries, err := filepath.Glob(filepath.Join(dir, ".cloudflared-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("staging files not cleaned up: %v, %v", entries, err)
			}
		})
	}
}
