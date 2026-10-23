// Package pcsetup hands out the scripts that prepare a PC for remote
// shutdown and Wake-on-LAN, and receives their report.
//
// It runs a small HTTP server on the router's LAN address only. A device
// gets a random one-hour token; the PC fetches its script by that token
// (`irm …/win | iex` or `curl …/linux | sudo bash`) and posts back the
// user name, OS and MAC when done. Nothing secret is served: the script
// carries the bot's public key.
package pcsetup

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"
)

//go:embed setup.ps1.tmpl
var ps1Tmpl string

//go:embed setup.sh.tmpl
var shTmpl string

//go:embed remove.ps1.tmpl
var ps1RemoveTmpl string

//go:embed remove.sh.tmpl
var shRemoveTmpl string

// Script kinds, also the URL suffixes.
const (
	Windows       = "win"
	Linux         = "linux"
	WindowsRemove = "win-remove"
	LinuxRemove   = "linux-remove"
)

var templates = map[string]*template.Template{
	Windows:       template.Must(template.New("ps1").Parse(ps1Tmpl)),
	Linux:         template.Must(template.New("sh").Parse(shTmpl)),
	WindowsRemove: template.Must(template.New("ps1rm").Parse(ps1RemoveTmpl)),
	LinuxRemove:   template.Must(template.New("shrm").Parse(shRemoveTmpl)),
}

func isWindows(kind string) bool { return kind == Windows || kind == WindowsRemove }

// FileName is the name a script gets when sent as a Telegram document.
func FileName(kind string) string {
	name := "owrt-bot-setup"
	if kind == WindowsRemove || kind == LinuxRemove {
		name = "owrt-bot-remove"
	}
	if isWindows(kind) {
		return name + ".ps1"
	}
	return name + ".sh"
}

// utf8BOM marks a saved .ps1 as UTF-8 for Windows PowerShell 5.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// TokenTTL is how long a setup link works.
const TokenTTL = time.Hour

// Request is an outstanding setup for one device.
type Request struct {
	DeviceID string
	Device   string // name, shown in the script header
	Expires  time.Time
}

// Report is what the script sends back.
type Report struct {
	User, OS, Host, MAC string
	IP                  string // the PC's address as seen by the router
	Removed             bool   // sent by a removal script
}

// Server serves scripts and receives reports.
type Server struct {
	RouterIP string // LAN address the server listens on and scripts call
	Port     int
	Key      func() string // the bot's authorized_keys line
	OnReport func(Request, Report)
	Log      *slog.Logger
	Now      func() time.Time

	mu     sync.Mutex
	tokens map[string]Request
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) base() string { return fmt.Sprintf("http://%s:%d", s.RouterIP, s.Port) }

// TokenFor returns the device's live token, creating one if needed, so
// opening the setup screen twice shows the same links.
func (s *Server) TokenFor(deviceID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = map[string]Request{}
	}
	now := s.now()
	for tok, r := range s.tokens {
		if now.After(r.Expires) {
			delete(s.tokens, tok)
		} else if r.DeviceID == deviceID && r.Expires.Sub(now) > 10*time.Minute {
			return tok
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.tokens[tok] = Request{DeviceID: deviceID, Device: name, Expires: now.Add(TokenTTL)}
	return tok
}

// ScriptURL is the link the PC fetches.
func (s *Server) ScriptURL(token, kind string) string {
	return s.base() + "/s/" + token + "/" + kind
}

func (s *Server) lookup(token string) (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.tokens[token]
	if !ok || s.now().After(r.Expires) {
		return Request{}, false
	}
	return r, true
}

func (s *Server) consume(token string) (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.tokens[token]
	if ok {
		delete(s.tokens, token)
	}
	return r, ok && !s.now().After(r.Expires)
}

// Script renders the script for token. Windows files saved to disk get a
// UTF-8 BOM, or PowerShell 5 would read the Russian messages as ANSI.
func (s *Server) Script(token, kind string, forFile bool) ([]byte, error) {
	r, ok := s.lookup(token)
	if !ok {
		return nil, errors.New("pcsetup: unknown or expired token")
	}
	t, ok := templates[kind]
	if !ok {
		return nil, fmt.Errorf("pcsetup: unknown script %q", kind)
	}
	var buf bytes.Buffer
	if forFile && isWindows(kind) {
		buf.Write(utf8BOM)
	}
	err := t.Execute(&buf, map[string]string{
		"Key":       s.Key(),
		"ReportURL": s.base() + "/s/" + token + "/done",
		"ScriptURL": s.ScriptURL(token, kind),
		"RouterIP":  s.RouterIP,
		"Device":    strings.NewReplacer("'", "", `"`, "", "\n", " ").Replace(r.Device),
	})
	if err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if isWindows(kind) {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out, nil
}

// Handler serves GET /s/{token}/{win|linux} and POST /s/{token}/done.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{token}/{kind}", func(w http.ResponseWriter, r *http.Request) {
		body, err := s.Script(r.PathValue("token"), r.PathValue("kind"), false)
		if err != nil {
			http.Error(w, "link expired, open the setup screen in the bot again", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("POST /s/{token}/done", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		rep := Report{
			User:    strings.TrimSpace(r.PostForm.Get("user")),
			OS:      r.PostForm.Get("os"),
			Host:    strings.TrimSpace(r.PostForm.Get("host")),
			MAC:     strings.TrimSpace(r.PostForm.Get("mac")),
			Removed: r.PostForm.Get("action") == "removed",
		}
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			rep.IP = host
		}
		if rep.User == "" || (rep.OS != "windows" && rep.OS != "linux") {
			http.Error(w, "user and os are required", http.StatusBadRequest)
			return
		}
		req, ok := s.consume(r.PathValue("token"))
		if !ok {
			http.Error(w, "link expired", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
		if s.OnReport != nil {
			go s.OnReport(req, rep)
		}
	})
	return mux
}

// Run listens on RouterIP:Port until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              net.JoinHostPort(s.RouterIP, fmt.Sprint(s.Port)),
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if s.Log != nil {
		s.Log.Info("PC setup server", "addr", srv.Addr)
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
