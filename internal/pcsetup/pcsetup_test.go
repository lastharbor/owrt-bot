package pcsetup

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newServer(now *time.Time, reports chan Report) *Server {
	return &Server{
		RouterIP: "192.168.1.1", Port: 8099,
		Key:      func() string { return "ssh-ed25519 AAAAtest owrt-bot" },
		Now:      func() time.Time { return *now },
		OnReport: func(_ Request, r Report) { reports <- r },
	}
}

func TestScriptsAndReport(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	reports := make(chan Report, 1)
	s := newServer(&now, reports)
	tok := s.TokenFor("d1", "Игровой ПК")
	if again := s.TokenFor("d1", "Игровой ПК"); again != tok {
		t.Fatal("second open of the screen made a new link")
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for kind, want := range map[string]string{
		Windows: "Add-WindowsCapability", Linux: "sudoers.d/owrt-bot",
		WindowsRemove: "Remove-LocalUser", LinuxRemove: "userdel",
	} {
		resp, err := http.Get(ts.URL + "/s/" + tok + "/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		text := string(body)
		parts := []string{want, "http://192.168.1.1:8099/s/" + tok + "/done", "Игровой ПК"}
		if kind == Windows || kind == Linux {
			parts = append(parts, "ssh-ed25519 AAAAtest owrt-bot", "restrict,command=")
		}
		for _, part := range parts {
			if !strings.Contains(text, part) {
				t.Errorf("%s script lacks %q", kind, part)
			}
		}
		if strings.Contains(text, "{{") {
			t.Errorf("%s script has unrendered template", kind)
		}
	}
	if b, _ := s.Script(tok, Windows, true); !bytes.HasPrefix(b, utf8BOM) || !strings.Contains(string(b), "\r\n") {
		t.Error("Windows file needs a BOM and CRLF")
	}

	resp, err := http.PostForm(ts.URL+"/s/"+tok+"/done", url.Values{"user": {"anton"}, "os": {"windows"}, "host": {"PC"}, "mac": {"02-5E-10-AA-BB-01"}})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("report: %v %v", err, resp.Status)
	}
	select {
	case r := <-reports:
		if r.User != "anton" || r.OS != "windows" || r.Host != "PC" || r.IP != "127.0.0.1" {
			t.Errorf("report = %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("OnReport not called")
	}
	resp, _ = http.PostForm(ts.URL+"/s/"+tok+"/done", url.Values{"user": {"x"}, "os": {"linux"}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("link reused: %v", resp.Status)
	}
}

func TestExpiredAndBadInput(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := newServer(&now, make(chan Report, 1))
	tok := s.TokenFor("d1", "PC")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, _ := http.PostForm(ts.URL+"/s/"+tok+"/done", url.Values{"user": {"x"}, "os": {"macos"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown OS accepted: %v", resp.Status)
	}
	if resp, _ := http.Get(ts.URL + "/s/" + tok + "/bsd"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown script kind served: %v", resp.Status)
	}
	now = now.Add(TokenTTL + time.Minute)
	if resp, _ := http.Get(ts.URL + "/s/" + tok + "/win"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("expired link served: %v", resp.Status)
	}
}

// TestDumpScripts writes rendered scripts to $PCSETUP_DUMP for syntax
// checks with the real interpreters (PowerShell parser, bash -n).
func TestDumpScripts(t *testing.T) {
	dir := os.Getenv("PCSETUP_DUMP")
	if dir == "" {
		t.Skip("set PCSETUP_DUMP to a directory")
	}
	now := time.Now()
	s := newServer(&now, make(chan Report, 1))
	tok := s.TokenFor("d1", "PC")
	for _, kind := range []string{Windows, Linux, WindowsRemove, LinuxRemove} {
		b, err := s.Script(tok, kind, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, kind+"-"+FileName(kind)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
