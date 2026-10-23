package wolui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

func (f *Feature) outcomeLine(t func(string, ...any) string, o wol.Outcome) string {
	name := esc(o.Device.Name)
	switch {
	case o.Err != nil:
		return t("wol_wake_err", "name", name) + " " + errText(t, o.Err)
	case o.Woke() && o.Elapsed == 0:
		return t("wol_already_on", "name", name)
	case o.Woke():
		return t("wol_woke", "name", name, "time", secs(t, o.Elapsed))
	default:
		return t("wol_not_woke", "name", name, "time", secs(t, o.Elapsed)) + "\n" + t("wol_not_woke_hint")
	}
}

func progressScreen(c *bot.Ctx, d wol.Device, el time.Duration, r *wol.Result) bot.Screen {
	text := c.T("wol_waking", "name", esc(d.Name), "time", secs(c.T, el))
	if r != nil {
		text += "\n" + statusText(c.T, *r)
	}
	return bot.Screen{
		Text:     text,
		Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_to_list"), "wol:list")).Build(),
	}
}

// wake starts a background wake so the chat stays responsive; the screen
// counts up and turns into the device card when it is done. If the user
// walks away meanwhile, the result arrives as a separate message.
func (f *Feature) wake(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	c.Toast(c.T("wol_sent_toast", "name", d.Name))
	c.Ack()
	_ = f.M.Store.AddEvent(wol.LogEntry{
		At:       f.now(),
		DeviceID: d.ID,
		Device:   d.Name,
		Kind:     wol.EventWake,
	})
	if err := c.Show(progressScreen(c, d, 0, nil)); err != nil {
		return err
	}
	c.Go(func(bg *bot.Ctx) {
		o := f.M.Wake(bg, d, func(el time.Duration, r wol.Result) {
			_ = bg.Show(progressScreen(bg, d, el, &r))
		})
		line := f.outcomeLine(bg.T, o)
		if err := f.showCard(bg, d, line); errors.Is(err, bot.ErrStale) {
			_ = bg.Notify(line)
		}
	})
	return nil
}

func (f *Feature) groupProgress(c *bot.Ctx, name string, devs []wol.Device, latest map[string]wol.Result, start time.Time) bot.Screen {
	lines := []string{c.T("wol_waking_group", "name", esc(name), "time", secs(c.T, time.Since(start))), ""}
	for _, d := range devs {
		r, ok := latest[d.ID]
		mark := "⏳"
		if ok {
			mark = r.Status.Emoji()
		}
		lines = append(lines, mark+" "+esc(d.Name))
	}
	return bot.Screen{
		Text:     strings.Join(lines, "\n"),
		Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_to_groups"), "wol:groups")).Build(),
	}
}

func (f *Feature) wakeGroup(c *bot.Ctx) error {
	name, devs, err := f.M.Resolve(wol.Target{Kind: wol.TargetGroup, ID: c.Arg(0)})
	if err != nil {
		c.Toast(c.T("wol_not_found"))
		return f.showGroups(c)
	}
	if len(devs) == 0 {
		c.Toast(c.T("wol_group_empty"))
		return f.showGroup(c)
	}
	c.Ack()
	start := time.Now()
	if err := c.Show(f.groupProgress(c, name, devs, nil, start)); err != nil {
		return err
	}
	c.Go(func(bg *bot.Ctx) {
		outs := f.M.WakeAll(bg, devs, func(latest map[string]wol.Result) {
			_ = bg.Show(f.groupProgress(bg, name, devs, latest, start))
		})
		summary := f.summary(bg.T, name, outs)
		kb := bot.NewKB().Row(bot.Btn(bg.T("btn_to_groups"), "wol:groups"), bot.Btn(bg.T("btn_to_list"), "wol:list")).Build()
		if err := bg.Show(bot.Screen{Text: summary, Keyboard: kb}); errors.Is(err, bot.ErrStale) {
			_ = bg.Notify(summary)
		}
	})
	return nil
}

func (f *Feature) summary(t func(string, ...any) string, name string, outs []wol.Outcome) string {
	lines := []string{t("wol_group_done", "name", esc(name)), ""}
	for _, o := range outs {
		lines = append(lines, f.outcomeLine(t, o))
	}
	return strings.Join(lines, "\n")
}

// ---- slash commands

// Commands send a fresh menu at the bottom, then keep it live.
func (f *Feature) cmdList(c *bot.Ctx) error {
	if err := c.Reply(f.listScreen(c, f.cacheAge())); err != nil {
		return err
	}
	return f.liveList(c, false)
}

func (f *Feature) cmdStatus(c *bot.Ctx) error {
	if err := c.Reply(f.listScreen(c, f.cacheAge())); err != nil {
		return err
	}
	return f.liveList(c, true)
}

// cmdWake handles "/wake name", "/wake group" and "/wake all".
func (f *Feature) cmdWake(c *bot.Ctx) error {
	q := strings.ToLower(c.CommandArgs())
	if q == "" {
		return c.Reply(f.listScreen(c, f.cacheAge()))
	}
	name, devs := f.match(q)
	if len(devs) == 0 {
		return c.Reply(bot.Screen{Text: c.T("wol_wake_nomatch", "q", esc(q)) + "\n\n" + f.listScreen(c, f.cacheAge()).Text,
			Keyboard: f.listScreen(c, f.cacheAge()).Keyboard})
	}
	start := time.Now()
	if err := c.Reply(f.groupProgress(c, name, devs, nil, start)); err != nil {
		return err
	}
	c.Go(func(bg *bot.Ctx) {
		outs := f.M.WakeAll(bg, devs, func(latest map[string]wol.Result) {
			_ = bg.Show(f.groupProgress(bg, name, devs, latest, start))
		})
		summary := f.summary(bg.T, name, outs)
		if err := bg.Show(bot.Screen{Text: summary, Keyboard: bot.NewKB().Row(bot.Btn(bg.T("btn_to_list"), "wol:list")).Build()}); errors.Is(err, bot.ErrStale) {
			_ = bg.Notify(summary)
		}
	})
	return nil
}

// match finds devices by exact name, then name prefix, then group name;
// "all"/"все" means every device.
func (f *Feature) match(q string) (string, []wol.Device) {
	devs := f.M.Store.Devices()
	if q == "all" || q == "все" {
		return q, devs
	}
	for _, d := range devs {
		if strings.ToLower(d.Name) == q {
			return d.Name, []wol.Device{d}
		}
	}
	for _, g := range f.M.Store.Groups() {
		if strings.ToLower(g.Name) == q {
			name, ds, _ := f.M.Resolve(wol.Target{Kind: wol.TargetGroup, ID: g.ID})
			return name, ds
		}
	}
	var hits []wol.Device
	for _, d := range devs {
		if strings.HasPrefix(strings.ToLower(d.Name), q) {
			hits = append(hits, d)
		}
	}
	if len(hits) == 1 {
		return hits[0].Name, hits
	}
	return "", nil
}

// ---- background hooks wired in main

// OnPresence reports watcher events to the chat, records them in the event log,
// and auto-deletes the chat notification after 10 seconds to keep the chat clean.
func (f *Feature) OnPresence(ev wol.Event) {
	var kind wol.EventKind
	switch ev.To {
	case wol.StatusOnline:
		kind = wol.EventOnline
	case wol.StatusAsleep:
		kind = wol.EventAsleep
	case wol.StatusOffline:
		kind = wol.EventOffline
	}
	if kind != "" {
		_ = f.M.Store.AddEvent(wol.LogEntry{
			At:       ev.At,
			DeviceID: ev.Device.ID,
			Device:   ev.Device.Name,
			Kind:     kind,
		})
	}

	key := map[wol.Status]string{
		wol.StatusOnline:  "wol_ev_online",
		wol.StatusAsleep:  "wol_ev_asleep",
		wol.StatusOffline: "wol_ev_offline",
	}[ev.To]
	if key == "" {
		return
	}
	f.Out.BroadcastTemp(context.Background(), f.T.T(key, "name", esc(ev.Device.Name), "time", ev.At.Format("15:04")), 10*time.Second)
}

// OnSchedule wakes a scheduled target and reports the outcome.
func (f *Feature) OnSchedule(ctx context.Context, s wol.Schedule) {
	name, devs, err := f.M.Resolve(s.Target)
	if err != nil || len(devs) == 0 {
		return
	}
	f.Out.Broadcast(ctx, f.T.T("wol_sched_fired", "name", esc(name), "at", s.At))
	outs := f.M.WakeAll(ctx, devs, nil)
	f.Out.Broadcast(ctx, f.summary(f.T.T, name, outs))
}

// targetName renders a schedule/group target for lists.
func (f *Feature) targetName(t func(string, ...any) string, tg wol.Target) string {
	name, _, err := f.M.Resolve(tg)
	if err != nil {
		return t("wol_target_gone")
	}
	if tg.Kind == wol.TargetGroup {
		return fmt.Sprintf("👥 %s", esc(name))
	}
	return "🖥 " + esc(name)
}
