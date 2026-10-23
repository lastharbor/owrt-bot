// Package system shows router health and lets the user restart services
// or the router itself.
package system

import (
	"fmt"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/sys"
	"github.com/lastharbor/owrt-bot/internal/ui"
)

type Feature struct {
	Proc     sys.Proc
	Metrics  *sys.Sampler
	Run      sys.Runner
	Services []sys.Service
}

var live = bot.LiveOptions{Every: 3 * time.Second}

func (f *Feature) Register(r *bot.Router) {
	r.Menu(10, "btn_system", "sys:show")
	r.Callback("sys:show", f.show)
	r.Callback("sys:svc", f.confirmRestart)
	r.Callback("sys:svcok", f.restart)
	r.Callback("sys:reboot", f.confirmReboot)
	r.Callback("sys:rebootok", f.reboot)
}

func kb(kb uint64) string {
	switch {
	case kb >= 1<<20:
		return fmt.Sprintf("%.1f GB", float64(kb)/(1<<20))
	case kb >= 1<<10:
		return fmt.Sprintf("%.0f MB", float64(kb)/(1<<10))
	}
	return fmt.Sprintf("%d KB", kb)
}

func uptime(d time.Duration) string {
	d = d.Truncate(time.Minute)
	return fmt.Sprintf("%dd %dh %dm", int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60)
}

// cpuSpark is the last ~minute of CPU% as a sparkline.
func (f *Feature) cpuSpark() (cur float64, spark string) {
	h := f.Metrics.History(30)
	vals := make([]float64, len(h))
	for i, s := range h {
		vals[i] = s.CPU
	}
	if len(h) > 0 {
		cur = h[len(h)-1].CPU
	}
	return cur, ui.Spark(vals, 0, 100)
}

func (f *Feature) screen(c *bot.Ctx, extra string) bot.Screen {
	s := f.Proc.Snapshot()
	cpu, spark := f.cpuSpark()
	lines := []string{
		"<b>" + c.T("sys_title") + "</b>", "",
		c.T("sys_cpu", "lvl", ui.Level(cpu, 60, 90), "pct", fmt.Sprintf("%.0f", cpu),
			"load", fmt.Sprintf("%.2f %.2f %.2f", s.Load1, s.Load5, s.Load15)),
		"<code>" + spark + "</code>",
	}
	if s.TempC > 0 {
		lines = append(lines, c.T("sys_temp", "lvl", ui.Level(s.TempC, 65, 80), "v", fmt.Sprintf("%.1f", s.TempC)))
	}
	lines = append(lines,
		c.T("sys_mem", "lvl", ui.Level(s.MemPercent(), 75, 90), "bar", ui.Bar(s.MemPercent(), 10),
			"used", kb(s.MemUsedKB), "total", kb(s.MemTotalKB), "pct", fmt.Sprintf("%.0f", s.MemPercent())),
		c.T("sys_disk", "lvl", ui.Level(s.DiskPercent(), 75, 90), "bar", ui.Bar(s.DiskPercent(), 10),
			"used", kb(s.DiskUsedKB), "total", kb(s.DiskTotalKB), "pct", fmt.Sprintf("%.0f", s.DiskPercent())),
		c.T("sys_uptime", "v", uptime(s.Uptime)),
	)
	k := bot.NewKB()
	if len(f.Services) > 0 {
		states := sys.ServiceStates(c, f.Run)
		lines = append(lines, "", "<b>"+c.T("sys_services")+"</b>")
		var btns []bot.Button
		for i, svc := range f.Services {
			mark := "⚪" // not managed by procd: state unknown
			if running, ok := states[svc.Name]; ok {
				mark = map[bool]string{true: "🟢", false: "🔴"}[running]
			}
			lines = append(lines, mark+" "+svc.Name)
			btns = append(btns, bot.Btn("🔁 "+svc.Name, "sys:svc", fmt.Sprint(i)))
		}
		k.Grid(2, btns...)
	}
	if extra != "" {
		lines = append(lines, "", extra)
	}
	k.Row(bot.Btn(c.T("btn_refresh"), "sys:show"), bot.Btn(c.T("btn_reboot_router"), "sys:reboot")).
		Row(bot.Btn(c.T("btn_back"), "menu:main"))
	return bot.Screen{Text: strings.Join(lines, "\n"), Keyboard: k.Build()}
}

func (f *Feature) show(c *bot.Ctx) error { return f.liveWith(c, "") }

// liveWith keeps the system screen updating, with an optional result line
// (e.g. "service restarted") kept under it.
func (f *Feature) liveWith(c *bot.Ctx, extra string) error {
	return c.Live(func(c *bot.Ctx) bot.Screen { return f.screen(c, extra) }, live)
}

func (f *Feature) service(c *bot.Ctx) (sys.Service, bool) {
	var i int
	if _, err := fmt.Sscan(c.Arg(0), &i); err != nil || i < 0 || i >= len(f.Services) {
		return sys.Service{}, false
	}
	return f.Services[i], true
}

func (f *Feature) confirmRestart(c *bot.Ctx) error {
	svc, ok := f.service(c)
	if !ok {
		return f.show(c)
	}
	return c.Show(bot.Screen{
		Text: c.T("sys_confirm_restart", "name", svc.Name),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes"), "sys:svcok", c.Arg(0)), bot.Btn(c.T("btn_cancel"), "sys:show")).Build(),
	})
}

func (f *Feature) restart(c *bot.Ctx) error {
	svc, ok := f.service(c)
	if !ok {
		return f.show(c)
	}
	c.Ack()
	_, err := f.Run.Run(c, "/etc/init.d/"+svc.Name, "restart")
	msg := c.T("sys_restarted", "name", svc.Name)
	if err != nil {
		msg = c.T("sys_restart_fail", "name", svc.Name) + " <code>" + err.Error() + "</code>"
	}
	return f.liveWith(c, msg)
}

func (f *Feature) confirmReboot(c *bot.Ctx) error {
	return c.Show(bot.Screen{
		Text: c.T("sys_confirm_reboot"),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes"), "sys:rebootok"), bot.Btn(c.T("btn_cancel"), "sys:show")).Build(),
	})
}

func (f *Feature) reboot(c *bot.Ctx) error {
	if err := c.Show(bot.Screen{Text: c.T("sys_rebooting")}); err != nil {
		return err
	}
	// Give Telegram the edit before the network goes away.
	_, err := f.Run.Run(c, "sh", "-c", "sleep 2; reboot")
	return err
}
