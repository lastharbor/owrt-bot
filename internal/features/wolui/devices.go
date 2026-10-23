package wolui

import (
	"fmt"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/telegram"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

// ---- list

// listScreen renders statuses no older than maxAge, probing the rest.
func (f *Feature) listScreen(c *bot.Ctx, maxAge time.Duration) bot.Screen {
	devs := f.M.Store.Devices()
	res := f.M.Presence.Snapshot(c, devs, maxAge)
	lines := []string{"<b>" + c.T("wol_title") + "</b>", ""}
	kb := bot.NewKB()
	if len(devs) == 0 {
		lines = append(lines, c.T("wol_empty"))
	}
	for _, d := range devs {
		r := res[d.ID]
		ip := r.IP
		if ip == "" {
			ip = "—"
		}
		lines = append(lines, fmt.Sprintf("%s <b>%s</b> · %s", r.Status.Emoji(), esc(d.Name), ip))
		kb.Row(
			bot.Btn(r.Status.Emoji()+" "+d.Name, "wol:dev", d.ID),
			bot.Btn("⚡", "wol:wake", d.ID),
		)
	}
	if len(devs) > 0 {
		lines = append(lines, "", c.T("wol_legend"))
	}
	kb.Row(bot.Btn(c.T("btn_wol_add"), "wol:add"), bot.Btn(c.T("btn_wol_groups"), "wol:groups"))
	kb.Row(bot.Btn(c.T("btn_wol_sched"), "wol:sched"), bot.Btn(c.T("btn_refresh"), "wol:refresh"))
	kb.Row(bot.Btn(c.T("btn_back"), "menu:main"))
	return bot.Screen{Text: strings.Join(lines, "\n"), Keyboard: kb.Build()}
}

// liveList keeps the list updating. The first frame uses the watcher's
// cache (instant); later frames re-probe anything older than 10 s.
// fresh forces a full probe on the first frame (the refresh button).
func (f *Feature) liveList(c *bot.Ctx, fresh bool) error {
	firstAge := f.cacheAge()
	if fresh {
		firstAge = 0
	}
	frame := 0
	return c.Live(func(c *bot.Ctx) bot.Screen {
		age := 10 * time.Second
		if frame == 0 {
			age = firstAge
		}
		frame++
		return f.listScreen(c, age)
	}, liveWOL)
}

func (f *Feature) showList(c *bot.Ctx, fresh bool) error { return f.liveList(c, fresh) }

// ---- device card

func (f *Feature) cardScreen(c *bot.Ctx, d wol.Device, r wol.Result, extra string) bot.Screen {
	ip := r.IP
	if ip == "" {
		ip = d.IP
	}
	if ip == "" {
		ip = "—"
	}
	notify := c.T("wol_notify_off")
	if d.Notify {
		notify = c.T("wol_notify_on")
	}
	power := c.T("wol_power_none")
	if d.Power != nil {
		power = c.T("wol_power_set", "os", string(d.Power.OS), "user", esc(d.Power.User))
	}
	text := fmt.Sprintf("<b>%s</b>\n%s\n\nMAC: <code>%s</code>\nIP: <code>%s</code>\n%s\n%s",
		esc(d.Name), statusText(c.T, r), d.MAC, ip, notify, power) + note(extra)

	kb := bot.NewKB().
		Row(bot.Btn(c.T("btn_wake"), "wol:wake", d.ID), bot.Btn(c.T("btn_check"), "wol:check", d.ID))
	if d.Power != nil {
		kb.Row(
			bot.Btn(c.T("btn_pw_off"), "wol:pw", d.ID, string(wol.ActShutdown)),
			bot.Btn(c.T("btn_pw_reboot"), "wol:pw", d.ID, string(wol.ActReboot)),
			bot.Btn(c.T("btn_pw_sleep"), "wol:pw", d.ID, string(wol.ActSleep)),
		)
	}
	notifyBtn := c.T("btn_notify_on")
	if d.Notify {
		notifyBtn = c.T("btn_notify_off")
	}
	// Until SSH is set up there are no ⏻ buttons, so offer the setup
	// itself where the user looks for "turn off".
	powerBtn := c.T("btn_power_add")
	if d.Power != nil {
		powerBtn = c.T("btn_power_setup")
	}
	kb.Row(bot.Btn(c.T("btn_rename"), "wol:ren", d.ID), bot.Btn("🌐 IP", "wol:ip", d.ID), bot.Btn("🔗 MAC", "wol:mac", d.ID)).
		Row(bot.Btn(notifyBtn, "wol:ntf", d.ID), bot.Btn(powerBtn, "wol:pwr", d.ID)).
		Row(bot.Btn(c.T("btn_delete"), "wol:del", d.ID)).
		Row(bot.Btn(c.T("btn_to_list"), "wol:list"))
	return bot.Screen{Text: text, Keyboard: kb.Build()}
}

// showCard renders d at once from the cache ("checking…" if there is
// none, since probing a powered-off host takes ~2 s) and then keeps the
// status live with real probes from the background.
func (f *Feature) showCard(c *bot.Ctx, d wol.Device, extra string) error {
	first := true
	return c.Live(func(c *bot.Ctx) bot.Screen {
		cur, err := f.M.Store.Device(d.ID)
		if err != nil {
			cur = d // deleted meanwhile: keep the last known card
		}
		var r wol.Result
		if first {
			r, _ = f.M.Presence.Cached(cur.ID, f.cacheAge())
			first = false
		} else {
			r = f.M.Presence.Check(c, cur)
		}
		return f.cardScreen(c, cur, r, extra)
	}, liveWOL)
}

func (f *Feature) showDevice(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	return f.showCard(c, d, "")
}

func (f *Feature) check(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	c.Ack()
	f.M.Presence.Check(c, d) // fills the cache the card reads first
	return f.showCard(c, d, c.T("wol_checked", "time", f.now().Format("15:04:05")))
}

// ---- add

func (f *Feature) showCandidates(c *bot.Ctx) error {
	cands := f.Discovery.Candidates(c)
	kb := bot.NewKB()
	shown := 0
	for _, cd := range cands {
		if shown == 30 {
			break
		}
		label := cd.Name
		if label == "" {
			label = cd.MAC[9:] // last three octets help tell nameless devices apart
		}
		if cd.IP != "" {
			label += " · " + cd.IP
		}
		kb.Row(bot.Btn(label, "wol:addc", bot.CompactMAC(cd.MAC)))
		shown++
	}
	kb.Row(bot.Btn(c.T("btn_manual"), "wol:addm")).
		Row(bot.Btn(c.T("btn_to_list"), "wol:list"))
	text := "<b>" + c.T("wol_add_title") + "</b>\n\n" + c.T("wol_add_hint")
	if len(cands) == 0 {
		text += "\n\n" + c.T("wol_add_none")
	}
	return c.Show(bot.Screen{Text: text, Keyboard: kb.Build()})
}

func (f *Feature) addCandidate(c *bot.Ctx) error {
	mac, err := wol.NormalizeMAC(bot.ExpandMAC(c.Arg(0)))
	if err != nil {
		return err
	}
	d := wol.Device{Name: mac, MAC: mac, Notify: true}
	for _, cd := range f.Discovery.Candidates(c) {
		if cd.MAC == mac {
			if cd.Name != "" {
				d.Name, _ = wol.CleanName(cd.Name)
			}
			d.IP = cd.IP
		}
	}
	added, err := f.M.Store.AddDevice(d)
	if err != nil {
		c.Toast(errText(c.T, err))
		return f.showCandidates(c)
	}
	c.Toast(c.T("wol_added", "name", added.Name))
	return f.showCard(c, added, c.T("wol_added_hint"))
}

func (f *Feature) askManual(c *bot.Ctx) error {
	return c.Ask("wol_new", "", bot.Screen{
		Text:     c.T("wol_ask_manual"),
		Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_cancel"), "wol:add")).Build(),
	})
}

// parseManual reads "Name with spaces AA:BB:CC:DD:EE:FF [192.168.1.5]".
func parseManual(text string) (wol.Device, error) {
	var d wol.Device
	var name []string
	for _, tok := range strings.Fields(text) {
		if d.MAC == "" {
			if mac, err := wol.NormalizeMAC(tok); err == nil {
				d.MAC = mac
				continue
			}
		}
		if d.IP == "" {
			if ip, err := wol.CleanIP(tok); err == nil && ip != "" {
				d.IP = ip
				continue
			}
		}
		name = append(name, tok)
	}
	if d.MAC == "" {
		return d, wol.ErrBadMAC
	}
	d.Name = d.MAC
	if len(name) > 0 {
		n, err := wol.CleanName(strings.Join(name, " "))
		if err != nil {
			return d, err
		}
		d.Name = n
	}
	d.Notify = true
	return d, nil
}

func (f *Feature) onManual(c *bot.Ctx) error {
	d, err := parseManual(c.Text)
	if err == nil {
		d, err = f.M.Store.AddDevice(d)
	}
	if err != nil {
		return c.Ask("wol_new", "", bot.Screen{
			Text:     errText(c.T, err) + "\n\n" + c.T("wol_ask_manual"),
			Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_cancel"), "wol:add")).Build(),
		})
	}
	return f.showCard(c, d, c.T("wol_added", "name", esc(d.Name))+"\n"+c.T("wol_added_hint"))
}

// ---- edit

func (f *Feature) askEdit(key, promptKey string) bot.Handler {
	return func(c *bot.Ctx) error {
		d, err := f.device(c, c.Arg(0))
		if err != nil {
			return nil
		}
		return c.Ask(key, d.ID, bot.Screen{
			Text:     c.T(promptKey, "name", esc(d.Name)),
			Keyboard: cancelTo(c, "wol:dev", d.ID),
		})
	}
}

func cancelTo(c *bot.Ctx, route string, args ...string) *telegram.InlineKeyboard {
	return bot.NewKB().Row(bot.Btn(c.T("btn_cancel"), route, args...)).Build()
}

// editField is the shared shape of the three "type a new value" prompts.
func (f *Feature) editField(c *bot.Ctx, key, promptKey string, apply func(*wol.Device, string) error) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	if err = apply(&d, c.Text); err == nil {
		err = f.M.Store.UpdateDevice(d)
	}
	if err != nil {
		return c.Ask(key, d.ID, bot.Screen{
			Text:     errText(c.T, err) + "\n\n" + c.T(promptKey, "name", esc(d.Name)),
			Keyboard: cancelTo(c, "wol:dev", d.ID),
		})
	}
	return f.showCard(c, d, c.T("wol_saved"))
}

func (f *Feature) onName(c *bot.Ctx) error {
	return f.editField(c, "wol_name", "wol_ask_name", func(d *wol.Device, s string) (err error) {
		d.Name, err = wol.CleanName(s)
		return
	})
}

func (f *Feature) onIP(c *bot.Ctx) error {
	return f.editField(c, "wol_ip", "wol_ask_ip", func(d *wol.Device, s string) (err error) {
		if strings.TrimSpace(s) == "-" {
			d.IP = ""
			return nil
		}
		d.IP, err = wol.CleanIP(s)
		return
	})
}

func (f *Feature) onMAC(c *bot.Ctx) error {
	return f.editField(c, "wol_mac", "wol_ask_mac", func(d *wol.Device, s string) (err error) {
		d.MAC, err = wol.NormalizeMAC(s)
		return
	})
}

func (f *Feature) toggleNotify(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	d.Notify = !d.Notify
	if err := f.M.Store.UpdateDevice(d); err != nil {
		return err
	}
	return f.showCard(c, d, "")
}

// ---- delete

func (f *Feature) confirmDelete(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	return c.Show(bot.Screen{
		Text: c.T("wol_confirm_delete", "name", esc(d.Name)),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes_delete"), "wol:delok", d.ID), bot.Btn(c.T("btn_cancel"), "wol:dev", d.ID)).
			Build(),
	})
}

func (f *Feature) delete(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	if err := f.M.Store.DeleteDevice(d.ID); err != nil {
		return err
	}
	f.M.Watcher.Forget(d.ID)
	c.Toast(c.T("wol_deleted", "name", d.Name))
	return f.showList(c, false)
}
