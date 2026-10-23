package wolui

import (
	"regexp"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

var userRe = regexp.MustCompile(`^[\w.\-\\@]{1,64}$`)

func (f *Feature) showPowerSetup(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	if f.Keys == nil {
		c.Toast(c.T("wol_err_nokey"))
		return f.showCard(c, d, c.T("wol_err_nokey"))
	}
	text := "<b>" + c.T("wol_power_title", "name", esc(d.Name)) + "</b>\n\n" + c.T("wol_power_help")
	if d.Power != nil {
		text += "\n\n" + c.T("wol_power_set", "os", string(d.Power.OS), "user", esc(d.Power.User))
	}
	kb := bot.NewKB().
		Row(bot.Btn("🪟 Windows", "wol:pwos", d.ID, string(wol.Windows)), bot.Btn("🐧 Linux", "wol:pwos", d.ID, string(wol.Linux))).
		Row(bot.Btn(c.T("btn_show_key"), "wol:key", d.ID)).
		RowIf(d.Power != nil, bot.Btn(c.T("btn_power_disable"), "wol:pwoff", d.ID)).
		Row(bot.Btn(c.T("btn_back"), "wol:dev", d.ID))
	return c.Show(bot.Screen{Text: text, Keyboard: kb.Build()})
}

func (f *Feature) askPowerUser(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	os := wol.OS(c.Arg(1))
	if os != wol.Windows && os != wol.Linux {
		return f.showPowerSetup(c)
	}
	return c.Ask("wol_user", d.ID+":"+string(os), bot.Screen{
		Text:     c.T("wol_ask_user", "name", esc(d.Name)),
		Keyboard: cancelTo(c, "wol:pwr", d.ID),
	})
}

func (f *Feature) onPowerUser(c *bot.Ctx) error {
	id, os, _ := strings.Cut(c.Arg(0), ":")
	d, err := f.device(c, id)
	if err != nil {
		return nil
	}
	user := strings.TrimSpace(c.Text)
	if !userRe.MatchString(user) {
		return c.Ask("wol_user", c.Arg(0), bot.Screen{
			Text:     c.T("wol_err_user") + "\n\n" + c.T("wol_ask_user", "name", esc(d.Name)),
			Keyboard: cancelTo(c, "wol:pwr", d.ID),
		})
	}
	d.Power = &wol.Power{User: user, OS: wol.OS(os)}
	if err := f.M.Store.UpdateDevice(d); err != nil {
		return err
	}
	return f.showCard(c, d, c.T("wol_power_saved"))
}

func (f *Feature) disablePower(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	d.Power = nil
	if err := f.M.Store.UpdateDevice(d); err != nil {
		return err
	}
	return f.showCard(c, d, c.T("wol_power_removed"))
}

func (f *Feature) showKey(c *bot.Ctx) error {
	if f.Keys == nil {
		c.Toast(c.T("wol_err_nokey"))
		return nil
	}
	back := bot.Btn(c.T("btn_back"), "wol:list")
	if id := c.Arg(0); id != "" {
		back = bot.Btn(c.T("btn_back"), "wol:pwr", id)
	}
	return c.Show(bot.Screen{
		Text:     c.T("wol_key_text", "key", esc(f.Keys.AuthorizedKey())),
		Keyboard: bot.NewKB().Row(back).Build(),
	})
}

var actionKeys = map[wol.Action]string{
	wol.ActShutdown: "wol_act_off",
	wol.ActReboot:   "wol_act_reboot",
	wol.ActSleep:    "wol_act_sleep",
}

func (f *Feature) confirmPower(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	a := wol.Action(c.Arg(1))
	key, ok := actionKeys[a]
	if !ok {
		return f.showCard(c, d, "")
	}
	return c.Show(bot.Screen{
		Text: c.T("wol_confirm_power", "action", c.T(key), "name", esc(d.Name)),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes"), "wol:pwgo", d.ID, string(a)), bot.Btn(c.T("btn_cancel"), "wol:dev", d.ID)).
			Build(),
	})
}

func (f *Feature) doPower(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	a := wol.Action(c.Arg(1))
	key, ok := actionKeys[a]
	if !ok {
		return f.showCard(c, d, "")
	}
	c.Ack()
	if err := f.M.PowerAction(c, d, a); err != nil {
		return f.showCard(c, d, c.T("wol_power_fail", "action", c.T(key))+" "+errText(c.T, err))
	}
	sent := c.T("wol_power_done", "action", c.T(key), "name", esc(d.Name))
	last, _ := f.M.Presence.Cached(d.ID, time.Hour)
	if err := c.Show(f.cardScreen(c, d, last, sent)); err != nil || a == wol.ActReboot {
		return err
	}
	// Watch it go down so the user knows the command actually worked.
	start := time.Now()
	c.Go(func(bg *bot.Ctx) {
		for time.Since(start) < 90*time.Second && !bg.Stale() {
			select {
			case <-bg.Done():
				return
			case <-time.After(3 * time.Second):
			}
			if r := f.M.Presence.Check(bg, d); r.Status != wol.StatusOnline {
				_ = bg.Show(f.cardScreen(bg, d, r, sent+"\n"+bg.T("wol_went_down", "time", secs(bg.T, time.Since(start)))))
				return
			}
		}
	})
	return nil
}
