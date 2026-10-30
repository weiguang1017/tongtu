package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"tongtu/internal/cf"
	"tongtu/internal/config"
)

// Intercept the production client's transport so these tests exercise the real
// Cloudflare request/response handling without contacting an external account.
type cloudflareFixture struct {
	modes       map[string]string
	dnsWrites   []string
	ingress     []cf.IngressRule
	tokenCalled bool
	failIngress bool
}

func (f *cloudflareFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.cloudflare.com" {
		return nil, fmt.Errorf("unexpected external request: %s", r.URL)
	}
	var result any = map[string]any{}
	status := http.StatusOK
	switch {
	case strings.Contains(r.URL.Path, "dns_records"):
		host := r.URL.Query().Get("name")
		if r.Method != http.MethodGet {
			var rec cf.DNSRecord
			if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
				return nil, err
			}
			host = rec.Name
			f.dnsWrites = append(f.dnsWrites, host)
			if f.modes[host] == "write-error" {
				status = http.StatusForbidden
			}
		} else {
			switch f.modes[host] {
			case "conflict":
				result = []cf.DNSRecord{{ID: "existing", Type: "A", Name: host, Content: "192.0.2.10"}}
			case "query-error":
				status = http.StatusForbidden
			default:
				result = []cf.DNSRecord{}
			}
		}
	case strings.HasSuffix(r.URL.Path, "/configurations"):
		var body struct {
			Config struct {
				Ingress []cf.IngressRule `json:"ingress"`
			} `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		f.ingress = body.Config.Ingress
		if f.failIngress {
			status = http.StatusBadGateway
		}
	case strings.HasSuffix(r.URL.Path, "/token"):
		f.tokenCalled = true
		result = "test-run-token"
	case strings.HasSuffix(r.URL.Path, "/cfd_tunnel") && r.Method == http.MethodGet:
		result = []cf.Tunnel{{ID: "tunnel", Name: r.URL.Query().Get("name")}}
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL)
	}
	body, err := json.Marshal(map[string]any{
		"success": status == http.StatusOK, "result": result,
		"errors": []map[string]any{{"code": 10000, "message": "fixture API failure"}},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
}

func installFixture(t *testing.T, f *cloudflareFixture) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func fixtureConfig() *config.Config {
	return &config.Config{
		Creds:   map[string]*config.Cred{"first": {Token: "test"}},
		Domains: map[string]*config.Domain{"example.test": {Cred: "first", ZoneID: "zone", AccountID: "account"}},
		Apps: map[string]*config.App{
			"broken":  {Hostname: "broken.example.test", Local: "127.0.0.1:8000", Proto: "http", Enabled: true},
			"healthy": {Hostname: "healthy.example.test", Local: "127.0.0.1:8001", Proto: "http", Enabled: true},
		},
	}
}

func TestSyncGroupIsolatesDNSFailures(t *testing.T) {
	for _, mode := range []string{"conflict", "query-error", "write-error", "missing"} {
		t.Run(mode, func(t *testing.T) {
			f := &cloudflareFixture{modes: map[string]string{"broken.example.test": mode}}
			installFixture(t, f)
			plan, err := Build(fixtureConfig(), nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := syncGroup(context.Background(), &plan.groups[0], "http://127.0.0.1:9999")
			if err != nil {
				t.Fatalf("application DNS problem blocked the group: %v", err)
			}
			want := []string{"healthy.example.test"}
			if mode == "missing" {
				want = []string{"broken.example.test", "healthy.example.test"}
				if len(res.appErrors) != 0 {
					t.Fatalf("missing DNS should be created: %v", res.appErrors)
				}
			} else if msg := res.appErrors["broken"]; !strings.Contains(msg, "重新添加") || !strings.Contains(msg, "其他应用不受影响") {
				t.Fatalf("missing actionable application warning: %q", msg)
			}
			if !reflect.DeepEqual(res.published, want) {
				t.Fatalf("published = %v; want %v", res.published, want)
			}
			if _, ok := res.appErrors["healthy"]; ok {
				t.Fatal("healthy application inherited another application's error")
			}
			var routed []string
			for _, rule := range f.ingress {
				if rule.Hostname != "" {
					routed = append(routed, rule.Hostname)
				}
			}
			if !reflect.DeepEqual(routed, want) || !f.tokenCalled {
				t.Fatalf("routed = %v, tokenCalled = %v", routed, f.tokenCalled)
			}
			if mode == "conflict" && !reflect.DeepEqual(f.dnsWrites, []string{"healthy.example.test"}) {
				t.Fatalf("conflicting A record was modified: %v", f.dnsWrites)
			}
		})
	}
}

func TestSyncGroupKeepsTunnelFailuresFatal(t *testing.T) {
	f := &cloudflareFixture{failIngress: true}
	installFixture(t, f)
	plan, err := Build(fixtureConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncGroup(context.Background(), &plan.groups[0], ""); err == nil {
		t.Fatal("tunnel-wide failure must still fail the group")
	}
}

func TestManagerDNSFailureLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("connector fixture uses a POSIX shell")
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\necho 'cloudflared version 2026.10.5'\nexit 0\nfi\nexec /bin/sleep 60\n"
	if err := os.WriteFile(filepath.Join(binDir, "cloudflared"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	f := &cloudflareFixture{modes: map[string]string{"broken.example.test": "conflict", "healthy.example.test": "conflict"}}
	installFixture(t, f)
	cfg := fixtureConfig()
	plan, err := Build(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Start(ctx, plan, nil); err != nil {
		t.Fatalf("all applications conflicting must not prevent connector startup: %v", err)
	}
	st := m.Status()
	if !st.Running || !st.Groups["first"].Alive || len(st.Groups["first"].Published) != 0 || len(st.AppErrors) != 2 {
		t.Fatalf("unexpected start state: %+v", st)
	}
	st.AppErrors["broken"] = "mutated"
	if m.Status().AppErrors["broken"] == "mutated" {
		t.Fatal("Status returned a mutable alias")
	}
	delete(f.modes, "healthy.example.test")
	if err := m.Reconcile(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st = m.Status()
	if len(st.AppErrors) != 1 || !reflect.DeepEqual(st.Groups["first"].Published, []string{"healthy.example.test"}) {
		t.Fatalf("recovered app not reflected in status: %+v", st)
	}
	broken := cfg.Apps["broken"]
	delete(cfg.Apps, "broken")
	if err := m.Reconcile(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if len(m.Status().AppErrors) != 0 {
		t.Fatal("deleted app left a stale warning")
	}
	delete(f.modes, "broken.example.test")
	cfg.Apps["broken"] = broken
	if err := m.Reconcile(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if st = m.Status(); len(st.AppErrors) != 0 || len(st.Groups["first"].Published) != 2 {
		t.Fatalf("re-added app did not publish: %+v", st)
	}
	m.Stop()
	if st = m.Status(); st.Running || len(st.AppErrors) != 0 {
		t.Fatalf("stop left stale state: %+v", st)
	}
}
