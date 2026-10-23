package wolui

import (
	"fmt"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

func (f *Feature) scheduleLine(t func(string, ...any) string, s wol.Schedule) string {
	state := "✅"
	if !s.Enabled {
		state = "⏸"
	}
	return fmt.Sprintf("%s ⏰ %s · %s → %s", state, s.At, days(t, s.Days), f.targetName(t, s.Target))
}

func (f *Feature) showSchedules(c *bot.Ctx) error {
	list := f.M.Store.Schedules()
	lines := []string{"<b>" + c.T("wol_sched_title") + "</b>", ""}
	kb := bot.NewKB()
	if len(list) == 0 {
		lines = append(lines, c.T("wol_sched_empty"))
	}
	for _, s := range list {
		lines = append(lines, f.scheduleLine(c.T, s))
		kb.Row(bot.Btn(fmt.Sprintf("⏰ %s · %s", s.At, days(c.T, s.Days)), "wol:sc", s.ID))
	}
	lines = append(lines, "", c.T("wol_sched_tz", "now", f.now().Format("15:04 MST")))
	kb.Row(bot.Btn(c.T("btn_sched_new"), "wol:scnew")).
		Row(bot.Btn(c.T("btn_to_list"), "wol:list"))
	return c.Show(bot.Screen{Text: strings.Join(lines, "\n"), Keyboard: kb.Build()})
}

func (f *Feature) pickScheduleTarget(c *bot.Ctx) error {
	kb := bot.NewKB()
	var btns []bot.Button
	for _, d := range f.M.Store.Devices() {
		btns = append(btns, bot.Btn("🖥 "+d.Name, "wol:sctgt", "d", d.ID))
	}
	for _, g := range f.M.Store.Groups() {
		btns = append(btns, bot.Btn("👥 "+g.Name, "wol:sctgt", "g", g.ID))
	}
	kb.Grid(2, btns...).Row(bot.Btn(c.T("btn_cancel"), "wol:sched"))
	text := c.T("wol_sched_pick")
	if len(btns) == 0 {
		text = c.T("wol_empty")
	}
	return c.Show(bot.Screen{Text: text, Keyboard: kb.Build()})
}

// Prompt arg encodes what the typed time is for: "new:d:<id>",
// "new:g:<id>" or "edit:<schedule id>".
func (f *Feature) askScheduleTime(c *bot.Ctx) error {
	return c.Ask("wol_sctime", "new:"+c.Arg(0)+":"+c.Arg(1), bot.Screen{
		Text: c.T("wol_ask_time"), Keyboard: cancelTo(c, "wol:sched"),
	})
}

func (f *Feature) askScheduleRetime(c *bot.Ctx) error {
	return c.Ask("wol_sctime", "edit:"+c.Arg(0), bot.Screen{
		Text: c.T("wol_ask_time"), Keyboard: cancelTo(c, "wol:sc", c.Arg(0)),
	})
}

func (f *Feature) onScheduleTime(c *bot.Ctx) error {
	at, err := wol.ParseClock(c.Text)
	if err != nil {
		return c.Ask("wol_sctime", c.Arg(0), bot.Screen{
			Text: errText(c.T, err) + "\n\n" + c.T("wol_ask_time"), Keyboard: cancelTo(c, "wol:sched"),
		})
	}
	parts := strings.Split(c.Arg(0), ":")
	var s wol.Schedule
	switch {
	case len(parts) == 3 && parts[0] == "new":
		kind := wol.TargetDevice
		if parts[1] == "g" {
			kind = wol.TargetGroup
		}
		s = wol.Schedule{Target: wol.Target{Kind: kind, ID: parts[2]}, Days: wol.Workdays, Enabled: true}
	case len(parts) == 2 && parts[0] == "edit":
		if s, err = f.M.Store.Schedule(parts[1]); err != nil {
			return f.showSchedules(c)
		}
	default:
		return f.showSchedules(c)
	}
	s.At = at
	s.LastRun = "" // a changed time may fire again today
	if s, err = f.M.Store.SaveSchedule(s); err != nil {
		return err
	}
	return c.Show(f.scheduleScreen(c, s))
}

func (f *Feature) schedule(c *bot.Ctx) (wol.Schedule, error) {
	s, err := f.M.Store.Schedule(c.Arg(0))
	if err != nil {
		c.Toast(c.T("wol_not_found"))
		_ = f.showSchedules(c)
	}
	return s, err
}

func (f *Feature) scheduleScreen(c *bot.Ctx, s wol.Schedule) bot.Screen {
	next := wol.Next(s, f.now())
	nextLine := c.T("wol_sched_off")
	if !next.IsZero() {
		nextLine = c.T("wol_sched_next", "when", c.T(fmt.Sprintf("day_%d", (int(next.Weekday())+6)%7))+" "+next.Format("02.01 15:04"))
	}
	text := fmt.Sprintf("<b>%s</b>\n\n%s\n%s", c.T("wol_sched_card"), f.scheduleLine(c.T, s), nextLine)

	var dayBtns []bot.Button
	for i := range 7 {
		wd := time.Weekday((i + 1) % 7)
		mark := "▫️"
		if s.Days.Has(wd) {
			mark = "✅"
		}
		dayBtns = append(dayBtns, bot.Btn(mark+c.T(fmt.Sprintf("day_%d", i)), "wol:scday", s.ID, fmt.Sprint(i)))
	}
	toggle := c.T("btn_pause")
	if !s.Enabled {
		toggle = c.T("btn_resume")
	}
	kb := bot.NewKB().
		Row(dayBtns[:4]...).
		Row(dayBtns[4:]...).
		Row(bot.Btn(c.T("btn_time"), "wol:sctime", s.ID), bot.Btn(toggle, "wol:scen", s.ID)).
		Row(bot.Btn(c.T("btn_delete"), "wol:scdel", s.ID)).
		Row(bot.Btn(c.T("btn_back"), "wol:sched"))
	return bot.Screen{Text: text, Keyboard: kb.Build()}
}

func (f *Feature) showSchedule(c *bot.Ctx) error {
	s, err := f.schedule(c)
	if err != nil {
		return nil
	}
	return c.Show(f.scheduleScreen(c, s))
}

func (f *Feature) toggleDay(c *bot.Ctx) error {
	s, err := f.schedule(c)
	if err != nil {
		return nil
	}
	var i int
	if _, err := fmt.Sscan(c.Arg(1), &i); err != nil || i < 0 || i > 6 {
		return c.Show(f.scheduleScreen(c, s))
	}
	s.Days = s.Days.Toggle(time.Weekday((i + 1) % 7))
	if s, err = f.M.Store.SaveSchedule(s); err != nil {
		return err
	}
	return c.Show(f.scheduleScreen(c, s))
}

func (f *Feature) toggleSchedule(c *bot.Ctx) error {
	s, err := f.schedule(c)
	if err != nil {
		return nil
	}
	s.Enabled = !s.Enabled
	if s, err = f.M.Store.SaveSchedule(s); err != nil {
		return err
	}
	return c.Show(f.scheduleScreen(c, s))
}

func (f *Feature) deleteSchedule(c *bot.Ctx) error {
	if err := f.M.Store.DeleteSchedule(c.Arg(0)); err != nil && err != wol.ErrNotFound {
		return err
	}
	c.Toast(c.T("wol_sched_deleted"))
	return f.showSchedules(c)
}
