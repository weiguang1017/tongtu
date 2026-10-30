package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"tongtu/internal/config"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunWithDNSConflictAndReAdd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("connector fixture uses a POSIX shell")
	}
	// Isolate configuration and subprocesses from the user's installed client.
	t.Setenv("HOME", t.TempDir())
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := os.WriteFile(filepath.Join(binDir, "cloudflared"), []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\necho 'cloudflared version 2026.10.5'\nexit 0\nfi\nexec /bin/sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.cloudflare.com" {
			return nil, fmt.Errorf("unexpected external request: %s", r.URL)
		}
		result := `{}`
		switch {
		case strings.Contains(r.URL.Path, "dns_records"):
			result = `[]`
			if r.URL.Query().Get("name") == "broken.example.test" {
				result = `[{"id":"occupied","type":"A","name":"broken.example.test","content":"192.0.2.10"}]`
			}
		case strings.HasSuffix(r.URL.Path, "/configurations"):
		case strings.HasSuffix(r.URL.Path, "/token"):
			result = `"fixture-token"`
		case strings.HasSuffix(r.URL.Path, "/cfd_tunnel"):
			result = `[{"id":"tunnel","name":"tongtu-first"}]`
		default:
			return nil, fmt.Errorf("unexpected request: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":` + result + `}`)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Creds["first"] = &config.Cred{Token: "fixture"}
	cfg.Domains["example.test"] = &config.Domain{Cred: "first", ZoneID: "zone", AccountID: "account"}
	for _, name := range []string{"broken", "healthy"} {
		cfg.Apps[name] = &config.App{Hostname: name + ".example.test", Local: "127.0.0.1:8000", Proto: "http", Enabled: true}
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	s, err := New("127.0.0.1:0", "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.mgr.Stop()
	call := func(handler http.HandlerFunc, method, path, body string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: HTTP %d: %s", method, path, w.Code, w.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	run := call(s.handleRun, "POST", "/api/run", `{}`)
	if run["ok"] != true || run["warning"] == nil || !reflect.DeepEqual(run["hostnames"], []any{"healthy.example.test"}) {
		t.Fatalf("startup response misreports partial publication: %v", run)
	}
	status := call(s.handleStatus, "GET", "/api/status", "")
	states := status["apps_state"].(map[string]any)
	if status["running"] != true || status["published_count"] != float64(1) || status["version"] != "1.1.0" {
		t.Fatalf("incorrect connector state or version: %v", status)
	}
	if broken := states["broken"].(map[string]any); broken["published"] != false || broken["app_error"] == nil {
		t.Fatalf("missing application error: %v", broken)
	}
	if healthy := states["healthy"].(map[string]any); healthy["published"] != true || healthy["app_error"] != nil {
		t.Fatalf("healthy application affected: %v", healthy)
	}
	call(s.handleAppItem, "DELETE", "/api/apps/broken", "")
	call(s.handleApps, "POST", "/api/apps", `{"name":"broken","hostname":"readded.example.test","local":"127.0.0.1:8000","proto":"http"}`)
	status = call(s.handleStatus, "GET", "/api/status", "")
	if status["running"] != true || status["published_count"] != float64(2) || status["last_error"] != nil {
		t.Fatalf("re-added application did not recover: %v", status)
	}
}
