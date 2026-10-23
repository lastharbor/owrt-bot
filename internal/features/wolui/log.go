package wolui

import (
	"fmt"
	"strings"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

func (f *Feature) cmdLog(c *bot.Ctx) error {
	return c.Reply(f.logScreen(c, ""))
}

func (f *Feature) showLog(c *bot.Ctx) error {
	devID := c.Arg(0)
	return c.Show(f.logScreen(c, devID))
}

func (f *Feature) clearLog(c *bot.Ctx) error {
	c.Ack()
	_ = f.M.Store.ClearEvents()
	c.Toast(c.T("wol_log_cleared"))
	return c.Show(f.logScreen(c, ""))
}

func (f *Feature) logScreen(c *bot.Ctx, devID string) bot.Screen {
	var events []wol.LogEntry
	var title string
	if devID != "" {
		if d, err := f.M.Store.Device(devID); err == nil {
			title = c.T("wol_log_title_dev", "name", esc(d.Name))
			events = f.M.Store.DeviceEvents(devID)
		}
	}
	if title == "" {
		title = c.T("wol_log_title")
		events = f.M.Store.Events()
	}

	lines := []string{"<b>" + title + "</b>", ""}
	if len(events) == 0 {
		lines = append(lines, c.T("wol_log_empty"))
	} else {
		count := 0
		for i := len(events) - 1; i >= 0 && count < 25; i-- {
			lines = append(lines, f.formatLogEntry(c.T, events[i]))
			count++
		}
	}

	kb := bot.NewKB()
	if devID != "" {
		kb.Row(bot.Btn(c.T("btn_refresh"), "wol:log", devID), bot.Btn(c.T("btn_back"), "wol:dev", devID))
	} else {
		kb.Row(bot.Btn(c.T("btn_refresh"), "wol:log"), bot.Btn(c.T("btn_clear"), "wol:logclr"))
		kb.Row(bot.Btn(c.T("btn_to_list"), "wol:list"))
	}
	return bot.Screen{Text: strings.Join(lines, "\n"), Keyboard: kb.Build()}
}

func (f *Feature) formatLogEntry(t func(string, ...any) string, ev wol.LogEntry) string {
	stamp := ev.At.Format("02.01 15:04")
	icon := "⚪"
	key := "wol_log_unknown"
	switch ev.Kind {
	case wol.EventOnline:
		icon = "🟢"
		key = "wol_log_online"
	case wol.EventOffline:
		icon = "🔴"
		key = "wol_log_offline"
	case wol.EventAsleep:
		icon = "💤"
		key = "wol_log_asleep"
	case wol.EventWake:
		icon = "⚡"
		key = "wol_log_wake"
	case wol.EventShutdown:
		icon = "⏻"
		key = "wol_log_shutdown"
	case wol.EventReboot:
		icon = "🔄"
		key = "wol_log_reboot"
	case wol.EventSleep:
		icon = "💤"
		key = "wol_log_sleep"
	}
	return fmt.Sprintf("%s <code>%s</code> <b>%s</b> — %s", icon, stamp, esc(ev.Device), t(key))
}
