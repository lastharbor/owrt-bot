// Package alerts watches router health and reports threshold crossings.
// It runs on its own goroutine, so a slow ping never delays a button.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lastharbor/owrt-bot/internal/config"
	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/sys"
)

type Broadcaster interface {
	Broadcast(ctx context.Context, text string)
}

type Pinger interface {
	Ping(ctx context.Context, ip string) bool
}

// Monitor reports only edges (ok -> firing -> ok), never a stuck state.
type Monitor struct {
	Cfg  config.Alerts
	Proc sys.Proc
	Ping Pinger
	T    *i18n.Translator
	Out  Broadcaster
	Log  *slog.Logger

	firing    map[string]bool
	wanDown   bool
	wanStreak int
	downAt    time.Time
}

func (m *Monitor) Run(ctx context.Context) {
	if !m.Cfg.Enabled {
		return
	}
	m.firing = map[string]bool{}
	t := time.NewTicker(m.Cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.check(ctx)
		}
	}
}

// edge records the new state and reports whether it just started firing.
func (m *Monitor) edge(key string, now bool) bool {
	was := m.firing[key]
	m.firing[key] = now
	return now && !was
}

func (m *Monitor) check(ctx context.Context) {
	s := m.Proc.Snapshot()
	if m.edge("cpu", s.CPUPercent() >= m.Cfg.CPUPercent) {
		m.Out.Broadcast(ctx, m.T.T("alert_cpu", "v", fmt.Sprintf("%.2f / %d (%.0f%%)", s.Load1, s.CPUs, s.CPUPercent())))
	}
	if m.edge("mem", s.MemPercent() >= m.Cfg.MemPercent) {
		m.Out.Broadcast(ctx, m.T.T("alert_mem", "v", fmt.Sprintf("%.0f", s.MemPercent())))
	}
	if m.edge("disk", s.DiskPercent() >= m.Cfg.DiskPercent) {
		m.Out.Broadcast(ctx, m.T.T("alert_disk", "v", fmt.Sprintf("%.0f", s.DiskPercent())))
	}
	if s.TempC > 0 && m.edge("temp", s.TempC >= m.Cfg.TempCelsius) {
		m.Out.Broadcast(ctx, m.T.T("alert_temp", "v", fmt.Sprintf("%.1f", s.TempC)))
	}
	m.checkWAN(ctx)
}

// checkWAN needs WANFailNeed failures in a row to declare WAN down and
// WANOkNeed successes to clear it, so one lost ping is not an outage.
// The "down" message is queued until the link is back (it could not be
// delivered while down anyway) and sent together with the recovery.
func (m *Monitor) checkWAN(ctx context.Context) {
	ok := m.Ping.Ping(ctx, m.Cfg.WANHost)
	if ok == !m.wanDown { // probe agrees with current state
		m.wanStreak = 0
		return
	}
	m.wanStreak++
	switch {
	case !m.wanDown && m.wanStreak >= m.Cfg.WANFailNeed:
		m.wanDown, m.wanStreak = true, 0
		m.Log.Warn("WAN down", "host", m.Cfg.WANHost)
		m.downAt = time.Now()
	case m.wanDown && m.wanStreak >= m.Cfg.WANOkNeed:
		m.wanDown, m.wanStreak = false, 0
		took := time.Since(m.downAt).Round(time.Second)
		m.Log.Info("WAN recovered", "host", m.Cfg.WANHost, "after", took)
		m.Out.Broadcast(ctx, m.T.T("alert_wan_back", "host", m.Cfg.WANHost,
			"since", m.downAt.Format("15:04"), "dur", took.String()))
	}
}
