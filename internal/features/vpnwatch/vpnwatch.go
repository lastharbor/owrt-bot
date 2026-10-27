// Package vpnwatch checks every route of a proxy engine through the route
// itself and reports routes that stop working and come back. It speaks the
// Clash API, which mihomo (Clash.Meta) and sing-box both implement, so it
// does not care which wrapper (ZeroBlock, Podkop, OpenClash, Nikki...)
// runs the engine.
//
// A tunnel can be up while the service behind it is not: on 2026-10-25
// WARP kept handshaking but moved its exit to Moscow, where YouTube is cut
// by SNI. So each group is tested with a URL of the service it carries,
// fetched by the engine through that group, not with a generic ping.
package vpnwatch

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lastharbor/owrt-bot/internal/config"
	"github.com/lastharbor/owrt-bot/internal/i18n"
)

type Broadcaster interface {
	Broadcast(ctx context.Context, text string)
}

// API is the part of the Clash API the monitor needs.
type API interface {
	Groups(ctx context.Context) ([]string, error)
	Delay(ctx context.Context, group, url string) (time.Duration, error)
}

// apiKey is the pseudo-group for "the Clash API itself does not answer".
const apiKey = "Clash API"

// Result is one group's check.
type Result struct {
	Section string // group name
	URL     string
	Delay   time.Duration
	Err     error
}

type state struct {
	down    bool
	streak  int
	since   time.Time
	lastErr string
}

// Monitor reports edges only: a section going down after FailNeed failed
// checks in a row, and coming back after OkNeed good ones.
type Monitor struct {
	Cfg config.VPNWatch
	API API
	T   *i18n.Translator
	Out Broadcaster
	Log *slog.Logger
	Now func() time.Time

	st map[string]*state
}

func (m *Monitor) Run(ctx context.Context) {
	if !m.Cfg.Enabled {
		return
	}
	t := time.NewTicker(m.Cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx)
		}
	}
}

// URL is what section is tested with.
func (m *Monitor) URL(section string) string {
	if u := m.Cfg.Checks[section]; u != "" {
		return u
	}
	return m.Cfg.DefaultURL
}

// CheckAll tests the groups in parallel: the ones named in the settings,
// or every top-level group. ErrNoController means there is nothing to check.
func (m *Monitor) CheckAll(ctx context.Context) ([]Result, error) {
	names, err := m.API.Groups(ctx)
	if err != nil {
		return nil, err
	}
	if len(m.Cfg.Groups) > 0 {
		names = m.Cfg.Groups
	}
	res := make([]Result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := m.URL(name)
			d, err := m.API.Delay(ctx, name, u)
			res[i] = Result{Section: name, URL: u, Delay: d, Err: err}
		}()
	}
	wg.Wait()
	return res, nil
}

// Tick runs one round of checks and sends at most two messages: one for
// everything that went down, one for everything that came back.
func (m *Monitor) Tick(ctx context.Context) {
	if m.st == nil {
		m.st = map[string]*state{}
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	res, err := m.CheckAll(ctx)
	switch {
	case errors.Is(err, ErrNoController):
		return
	case err != nil:
		// The API itself is silent: the engine is stopped or restarting.
		// Per-group states are kept, so a short restart changes nothing.
		res = []Result{{Section: apiKey, Err: err}}
	default:
		res = append(res, Result{Section: apiKey})
		m.forgetMissing(res)
	}

	var down, back []string
	for _, r := range res {
		s := m.st[r.Section]
		if s == nil {
			s = &state{}
			m.st[r.Section] = s
		}
		ok := r.Err == nil
		if ok == !s.down {
			s.streak = 0
			continue
		}
		s.streak++
		if !ok {
			s.lastErr = shortErr(r.Err)
		}
		switch {
		case !s.down && s.streak >= m.Cfg.FailNeed:
			s.down, s.streak, s.since = true, 0, now
			m.Log.Warn("vpn section down", "section", r.Section, "url", r.URL, "err", r.Err)
			down = append(down, m.T.T("vpn_line_down", "name", html.EscapeString(r.Section), "host", html.EscapeString(host(r.URL)), "err", html.EscapeString(s.lastErr)))
		case s.down && s.streak >= m.Cfg.OkNeed:
			took := now.Sub(s.since).Round(time.Minute)
			s.down, s.streak = false, 0
			m.Log.Info("vpn section back", "section", r.Section, "after", took)
			back = append(back, m.T.T("vpn_line_back", "name", html.EscapeString(r.Section),
				"since", s.since.Format("15:04"), "dur", dur(took)))
		}
	}
	if len(down) > 0 {
		m.Out.Broadcast(ctx, m.T.T("vpn_down")+"\n"+strings.Join(down, "\n"))
	}
	if len(back) > 0 {
		m.Out.Broadcast(ctx, m.T.T("vpn_back")+"\n"+strings.Join(back, "\n"))
	}
}

// forgetMissing drops groups that were removed from the engine, so a
// deleted group cannot stay "down" forever.
func (m *Monitor) forgetMissing(res []Result) {
	seen := make(map[string]bool, len(res))
	for _, r := range res {
		seen[r.Section] = true
	}
	for name := range m.st {
		if !seen[name] {
			delete(m.st, name)
		}
	}
}

// Report formats one round for the -vpncheck flag.
func Report(res []Result) string {
	sort.Slice(res, func(i, j int) bool { return res[i].Section < res[j].Section })
	w := 0
	for _, r := range res {
		w = max(w, len(r.Section))
	}
	var b strings.Builder
	for _, r := range res {
		if r.Err != nil {
			fmt.Fprintf(&b, "%-*s  FAIL  %-40s %s\n", w, r.Section, r.URL, shortErr(r.Err))
			continue
		}
		fmt.Fprintf(&b, "%-*s  ok    %-40s %s\n", w, r.Section, r.URL, r.Delay.Round(time.Millisecond))
	}
	return b.String()
}

func host(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	h, _, _ := strings.Cut(u, "/")
	return h
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}

// dur prints 1h5m rather than 1h5m0s; under a minute rounds up to 1m.
func dur(d time.Duration) string {
	if d < time.Minute {
		d = time.Minute
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}
