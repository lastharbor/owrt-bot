// Package wolui is the Telegram UI for Wake-on-LAN. It only translates
// button presses into wol.Manager calls and renders screens; all
// behaviour lives in internal/wol.
package wolui

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

// Broadcaster sends a message to every allowed chat.
type Broadcaster interface {
	Broadcast(ctx context.Context, text string)
	BroadcastTemp(ctx context.Context, text string, ttl time.Duration)
}

// KeyProvider exposes the public SSH key users must install on PCs.
type KeyProvider interface {
	AuthorizedKey() string
}

// Feature wires WOL screens into the bot.
type Feature struct {
	M         *wol.Manager
	Discovery wol.Discovery
	Keys      KeyProvider  // nil when power control is unavailable
	Setup     ScriptServer // nil when the LAN script server is not running
	T         *i18n.Translator
	Out       Broadcaster
	CacheAge  time.Duration // how old a watcher observation may be on list screens
	Now       func() time.Time
}

func (f *Feature) Register(r *bot.Router) {
	r.Menu(20, "btn_wol", "wol:list")
	r.Command("wol", "cmd_wol", f.cmdList)
	r.Command("wake", "cmd_wake", f.cmdWake)
	r.Command("status", "cmd_status", f.cmdStatus)
	r.Command("wollog", "cmd_wollog", f.cmdLog)

	// devices
	r.Callback("wol:list", func(c *bot.Ctx) error { return f.showList(c, false) })
	r.Callback("wol:refresh", func(c *bot.Ctx) error { c.Ack(); return f.showList(c, true) })
	r.Callback("wol:log", f.showLog)
	r.Callback("wol:logclr", f.clearLog)
	r.Callback("wol:dev", f.showDevice)
	r.Callback("wol:check", f.check)
	r.Callback("wol:add", f.showCandidates)
	r.Callback("wol:addc", f.addCandidate)
	r.Callback("wol:addm", f.askManual)
	r.Callback("wol:ren", f.askEdit("wol_name", "wol_ask_name"))
	r.Callback("wol:ip", f.askEdit("wol_ip", "wol_ask_ip"))
	r.Callback("wol:mac", f.askEdit("wol_mac", "wol_ask_mac"))
	r.Callback("wol:ntf", f.toggleNotify)
	r.Callback("wol:del", f.confirmDelete)
	r.Callback("wol:delok", f.delete)
	r.Prompt("wol_new", f.onManual)
	r.Prompt("wol_name", f.onName)
	r.Prompt("wol_ip", f.onIP)
	r.Prompt("wol_mac", f.onMAC)

	// wake
	r.Callback("wol:wake", f.wake)
	r.Callback("wol:gwake", f.wakeGroup)

	// power
	r.Callback("wol:pwr", f.showPowerSetup)
	r.Callback("wol:pwman", f.showPowerManual)
	r.Callback("wol:pwrm", f.showPowerRemove)
	r.Callback("wol:pwfile", f.sendScript)
	r.Callback("wol:pwos", f.askPowerUser)
	r.Callback("wol:pwoff", f.disablePower)
	r.Callback("wol:key", f.showKey)
	r.Callback("wol:pw", f.confirmPower)
	r.Callback("wol:pwgo", f.doPower)
	r.Prompt("wol_user", f.onPowerUser)

	// groups
	r.Callback("wol:groups", f.showGroups)
	r.Callback("wol:gnew", f.askGroupName)
	r.Callback("wol:grp", f.showGroup)
	r.Callback("wol:gtog", f.toggleMember)
	r.Callback("wol:gren", f.askGroupRename)
	r.Callback("wol:gdel", f.confirmGroupDelete)
	r.Callback("wol:gdelok", f.deleteGroup)
	r.Prompt("wol_gname", f.onGroupName)

	// schedules
	r.Callback("wol:sched", f.showSchedules)
	r.Callback("wol:scnew", f.pickScheduleTarget)
	r.Callback("wol:sctgt", f.askScheduleTime)
	r.Callback("wol:sc", f.showSchedule)
	r.Callback("wol:scday", f.toggleDay)
	r.Callback("wol:scen", f.toggleSchedule)
	r.Callback("wol:sctime", f.askScheduleRetime)
	r.Callback("wol:scdel", f.deleteSchedule)
	r.Prompt("wol_sctime", f.onScheduleTime)
}

// liveWOL: presence probes are cheaper than Telegram edits, 5 s is plenty.
var liveWOL = bot.LiveOptions{Every: 5 * time.Second}

// ---- shared helpers

func esc(s string) string { return html.EscapeString(s) }

func (f *Feature) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Feature) cacheAge() time.Duration {
	if f.CacheAge > 0 {
		return f.CacheAge
	}
	return 45 * time.Second
}

// device loads Args[0]; a missing device sends the user back to the list.
func (f *Feature) device(c *bot.Ctx, id string) (wol.Device, error) {
	d, err := f.M.Store.Device(id)
	if err != nil {
		c.Toast(c.T("wol_not_found"))
		_ = f.showList(c, false)
	}
	return d, err
}

func statusText(t func(string, ...any) string, r wol.Result) string {
	if r.At.IsZero() { // no observation yet
		return "⏳ " + t("wol_st_checking")
	}
	keys := map[wol.Status]string{
		wol.StatusOnline:  "wol_st_online",
		wol.StatusAsleep:  "wol_st_asleep",
		wol.StatusOffline: "wol_st_offline",
		wol.StatusUnknown: "wol_st_unknown",
	}
	return r.Status.Emoji() + " " + t(keys[r.Status])
}

func secs(t func(string, ...any) string, d time.Duration) string {
	return t("unit_sec", "n", int(d.Round(time.Second).Seconds()))
}

// errText maps domain errors to user-facing text.
func errText(t func(string, ...any) string, err error) string {
	switch {
	case errors.Is(err, wol.ErrBadMAC):
		return t("wol_err_mac")
	case errors.Is(err, wol.ErrBadName):
		return t("wol_err_name")
	case errors.Is(err, wol.ErrBadIP):
		return t("wol_err_ip")
	case errors.Is(err, wol.ErrBadTime):
		return t("wol_err_time")
	case errors.Is(err, wol.ErrDuplicate):
		return t("wol_err_dup")
	case errors.Is(err, wol.ErrHostKeyChanged):
		return t("wol_err_hostkey")
	case errors.Is(err, wol.ErrNoPower):
		return t("wol_err_nopower")
	case err != nil && strings.Contains(err.Error(), "unable to authenticate"):
		return t("wol_err_auth")
	case err != nil && (strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "i/o timeout")):
		return t("wol_err_ssh_down")
	case err != nil:
		return t("error_generic") + " <code>" + esc(err.Error()) + "</code>"
	}
	return ""
}

// note formats an optional result line appended under a screen.
func note(s string) string {
	if s == "" {
		return ""
	}
	return "\n\n" + s
}

// days renders a weekday set Monday-first ("Пн Вт Ср Чт Пт").
func days(t func(string, ...any) string, w wol.Weekdays) string {
	if w == wol.AllWeek {
		return t("days_all")
	}
	if w == wol.Workdays {
		return t("days_work")
	}
	var out []string
	for i := range 7 {
		wd := time.Weekday((i + 1) % 7)
		if w.Has(wd) {
			out = append(out, t(fmt.Sprintf("day_%d", i)))
		}
	}
	if len(out) == 0 {
		return t("days_none")
	}
	return strings.Join(out, " ")
}
