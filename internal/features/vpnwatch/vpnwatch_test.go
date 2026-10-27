package vpnwatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lastharbor/owrt-bot/internal/config"
	"github.com/lastharbor/owrt-bot/internal/i18n"
)

type fakeAPI struct {
	sections []string
	listErr  error
	fail     map[string]bool
	urls     map[string]string
	mu       sync.Mutex
}

func (f *fakeAPI) Groups(context.Context) ([]string, error) { return f.sections, f.listErr }

func (f *fakeAPI) Delay(_ context.Context, s, u string) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.urls == nil {
		f.urls = map[string]string{}
	}
	f.urls[s] = u
	if f.fail[s] {
		return 0, errors.New("timeout")
	}
	return 50 * time.Millisecond, nil
}

type sink struct{ msgs []string }

func (s *sink) Broadcast(_ context.Context, t string) { s.msgs = append(s.msgs, t) }

func newMon(api API, out *sink, now *time.Time) *Monitor {
	return &Monitor{
		Cfg: config.VPNWatch{Enabled: true, FailNeed: 2, OkNeed: 2,
			DefaultURL: "https://www.gstatic.com/generate_204",
			Checks:     map[string]string{"warp_awg": "https://www.youtube.com/generate_204"}},
		API: api, T: i18n.New("en").Add("en", "English", i18n.EN), Out: out,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return *now },
	}
}

func TestEdgesAndGrouping(t *testing.T) {
	api := &fakeAPI{sections: []string{"usa_awg", "warp_awg", "nl_awg"}, fail: map[string]bool{}}
	out := &sink{}
	now := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	m := newMon(api, out, &now)

	m.Tick(context.Background())
	if api.urls["warp_awg"] != "https://www.youtube.com/generate_204" || api.urls["usa_awg"] != m.Cfg.DefaultURL {
		t.Fatalf("urls = %v", api.urls)
	}

	api.fail["warp_awg"], api.fail["nl_awg"] = true, true
	m.Tick(context.Background()) // one failure is not an outage
	if len(out.msgs) != 0 {
		t.Fatalf("alerted after one failure: %q", out.msgs)
	}
	m.Tick(context.Background())
	if len(out.msgs) != 1 || !strings.Contains(out.msgs[0], "warp_awg") || !strings.Contains(out.msgs[0], "nl_awg") ||
		!strings.Contains(out.msgs[0], "www.youtube.com") {
		t.Fatalf("want one grouped down message, got %q", out.msgs)
	}
	m.Tick(context.Background()) // still down: no repeat
	if len(out.msgs) != 1 {
		t.Fatalf("repeated: %q", out.msgs)
	}

	api.fail = map[string]bool{}
	now = now.Add(65 * time.Minute)
	m.Tick(context.Background())
	m.Tick(context.Background())
	if len(out.msgs) != 2 || !strings.Contains(out.msgs[1], "20:00") || !strings.Contains(out.msgs[1], "1h5m") {
		t.Fatalf("want recovery with since and duration, got %q", out.msgs)
	}
}

func TestZeroBlockDownAndMissing(t *testing.T) {
	api := &fakeAPI{sections: []string{"usa_awg"}, fail: map[string]bool{}}
	out := &sink{}
	now := time.Now()
	m := newMon(api, out, &now)

	api.listErr = ErrNoController // no engine with a Clash API: silence
	m.Tick(context.Background())
	m.Tick(context.Background())
	if len(out.msgs) != 0 {
		t.Fatalf("alerted without an engine: %q", out.msgs)
	}

	api.listErr = errors.New("connection refused")
	m.Tick(context.Background())
	m.Tick(context.Background())
	if len(out.msgs) != 1 || !strings.Contains(out.msgs[0], apiKey) {
		t.Fatalf("want Clash API down, got %q", out.msgs)
	}

	// A section that was down and then deleted must not hang around.
	api.listErr = nil
	api.fail["usa_awg"] = true
	m.Tick(context.Background())
	m.Tick(context.Background())
	api.sections = nil
	m.Tick(context.Background())
	if _, ok := m.st["usa_awg"]; ok {
		t.Fatal("removed section still tracked")
	}
}

func TestClash(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/proxies":
			// sing-box shape as ZeroBlock builds it: GLOBAL lists everything,
			// the auto pool is nested in its section's selector.
			io.WriteString(w, `{"proxies":{
				"GLOBAL":{"type":"Fallback","all":["usa_awg__section","zen__section","zen__section__auto","direct-out"]},
				"usa_awg__section":{"type":"Selector","all":["usa_awg-vpn"]},
				"zen__section":{"type":"Selector","all":["zen__section__auto","US1","US2"]},
				"zen__section__auto":{"type":"URLTest","all":["US1","US2"]},
				"US1":{"type":"VLESS"},"US2":{"type":"VLESS"},"usa_awg-vpn":{"type":"Direct"},"direct-out":{"type":"Direct"}}}`)
		case strings.HasSuffix(r.URL.Path, "/delay"):
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			if strings.Contains(r.URL.Path, "zen") {
				w.WriteHeader(http.StatusGatewayTimeout)
				io.WriteString(w, `{"message":"Timeout"}`)
				return
			}
			io.WriteString(w, `{"delay":123}`)
		}
	}))
	defer srv.Close()

	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	c := &Clash{Endpoint: StaticEndpoint("0.0.0.0:"+port, "s3")}

	names, err := c.Groups(context.Background())
	if err != nil || strings.Join(names, ",") != "usa_awg__section,zen__section" {
		t.Fatalf("Groups = %v, %v", names, err)
	}
	if gotAuth != "Bearer s3" {
		t.Fatalf("auth = %q", gotAuth)
	}
	d, err := c.Delay(context.Background(), "usa_awg__section", "https://api.telegram.org/")
	if err != nil || d != 123*time.Millisecond {
		t.Fatalf("Delay = %v, %v", d, err)
	}
	if gotPath != "/proxies/usa_awg__section/delay" || !strings.Contains(gotQuery, "url=https%3A%2F%2Fapi.telegram.org%2F") {
		t.Fatalf("request = %s?%s", gotPath, gotQuery)
	}
	if _, err := c.Delay(context.Background(), "zen__section", "https://gemini.google.com/"); err == nil || err.Error() != "Timeout" {
		t.Fatalf("want Timeout, got %v", err)
	}
}

func TestTopGroupsMihomo(t *testing.T) {
	// mihomo: a user selector holding an url-test pool and DIRECT.
	got := topGroups(map[string]proxyInfo{
		"GLOBAL":    {Type: "Selector", All: []string{"Proxy", "Auto", "DIRECT"}},
		"Proxy":     {Type: "Selector", All: []string{"Auto", "HK", "DIRECT"}},
		"Auto":      {Type: "URLTest", All: []string{"HK"}},
		"Streaming": {Type: "Fallback", All: []string{"HK"}},
		"HK":        {Type: "Shadowsocks"},
		"DIRECT":    {Type: "Direct"},
	})
	if strings.Join(got, ",") != "Proxy,Streaming" {
		t.Fatalf("topGroups = %v", got)
	}
}

// fakeProc writes /proc/<pid>/cmdline files under a temp dir.
func fakeProc(t *testing.T, procs map[string][]string) string {
	root := t.TempDir()
	for pid, args := range procs {
		os.MkdirAll(filepath.Join(root, pid), 0o755)
		os.WriteFile(filepath.Join(root, pid, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "self"), 0o755) // non-numeric entries are skipped
	return root
}

func TestDiscoverSingBox(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "00-log.json"), []byte(`{"log":{}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "01-experimental.json"),
		[]byte(`{"experimental":{"clash_api":{"external_controller":"0.0.0.0:9090","secret":"abc"}}}`), 0o644)
	root := fakeProc(t, map[string][]string{
		"10":  {"/sbin/procd"},
		"300": {"sing-box", "run", "-C", dir, "-D", "/tmp/zeroblock"},
	})
	ep, err := Discover(root)()
	if err != nil || ep.Base != "http://127.0.0.1:9090" || ep.Secret != "abc" || !strings.Contains(ep.Source, "pid 300") {
		t.Fatalf("Discover = %+v, %v", ep, err)
	}
}

func TestDiscoverMihomo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"mixed-port: 7890\nexternal-controller: '0.0.0.0:9093'   # dashboard\nsecret: \"p4ss\"\ndns:\n  secret: nested-not-this\n"), 0o644)
	root := fakeProc(t, map[string][]string{"77": {"/usr/bin/mihomo", "-d", dir}})
	ep, err := Discover(root)()
	if err != nil || ep.Base != "http://127.0.0.1:9093" || ep.Secret != "p4ss" {
		t.Fatalf("Discover = %+v, %v", ep, err)
	}

	// Command-line flags win over the file, as in mihomo itself.
	root = fakeProc(t, map[string][]string{"78": {"/etc/openclash/core/clash_meta", "-d", dir, "-ext-ctl", "127.0.0.1:9999", "-secret", "cli"}})
	ep, err = Discover(root)()
	if err != nil || ep.Base != "http://127.0.0.1:9999" || ep.Secret != "cli" {
		t.Fatalf("Discover with flags = %+v, %v", ep, err)
	}
}

func TestDiscoverNothing(t *testing.T) {
	root := fakeProc(t, map[string][]string{"1": {"/sbin/procd"}, "2": {"sing-box", "run", "-c", "/nonexistent.json"}})
	if _, err := Discover(root)(); !errors.Is(err, ErrNoController) {
		t.Fatalf("want ErrNoController, got %v", err)
	}
}

func TestConfiguredGroups(t *testing.T) {
	api := &fakeAPI{sections: []string{"a", "b", "c"}, fail: map[string]bool{}}
	now := time.Now()
	m := newMon(api, &sink{}, &now)
	m.Cfg.Groups = []string{"b"}
	res, err := m.CheckAll(context.Background())
	if err != nil || len(res) != 1 || res[0].Section != "b" {
		t.Fatalf("CheckAll = %+v, %v", res, err)
	}
}
