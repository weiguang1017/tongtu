package landing

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestServePage(t *testing.T) {
	s, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if !strings.HasPrefix(s.Service(), "http://127.0.0.1:") {
		t.Fatalf("Service() = %q, 期望 http://127.0.0.1:<port>", s.Service())
	}
	// 任意路径、任意 Host 都应返回宣传页
	resp, err := http.Get(s.Service() + "/some/random/path")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Robots-Tag"); got != "noindex" {
		t.Fatalf("X-Robots-Tag = %q, 期望 noindex", got)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"通途", "天堑变通途", "install.sh"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("页面缺少关键内容 %q", want)
		}
	}
}
