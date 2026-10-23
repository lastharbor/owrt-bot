package wolui

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/pcsetup"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

var userRe = regexp.MustCompile(`^[\w.\-\\@]{1,64}$`)

// ScriptServer hands out setup/removal scripts for a device (pcsetup).
type ScriptServer interface {
	TokenFor(deviceID, name string) string
	ScriptURL(token, kind string) string
	Script(token, kind string, forFile bool) ([]byte, error)
}

// showPowerSetup offers the one-command setup when the script server is
// running, and the manual steps otherwise.
func (f *Feature) showPowerSetup(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	if f.Keys == nil {
		c.Toast(c.T("wol_err_nokey"))
		return f.showCard(c, d, c.T("wol_err_nokey"))
	}
	if f.Setup == nil {
		return f.showPowerManual(c)
	}
	tok := f.Setup.TokenFor(d.ID, d.Name)
	text := "<b>" + c.T("wol_power_title", "name", esc(d.Name)) + "</b>\n\n" +
		c.T("wol_power_auto",
			"win", esc(f.Setup.ScriptURL(tok, pcsetup.Windows)),
			"linux", esc(f.Setup.ScriptURL(tok, pcsetup.Linux)))
	if d.Power != nil {
		text += "\n\n" + c.T("wol_power_set", "os", string(d.Power.OS), "user", esc(d.Power.User))
	}
	kb := bot.NewKB().
		Row(bot.Btn(c.T("btn_script_win"), "wol:pwfile", d.ID, pcsetup.Windows), bot.Btn(c.T("btn_script_linux"), "wol:pwfile", d.ID, pcsetup.Linux)).
		RowIf(d.Power != nil, bot.Btn(c.T("btn_power_remove"), "wol:pwrm", d.ID)).
		Row(bot.Btn(c.T("btn_power_manual"), "wol:pwman", d.ID)).
		Row(bot.Btn(c.T("btn_back"), "wol:dev", d.ID))
	return c.Show(bot.Screen{Text: text, Keyboard: kb.Build()})
}

// showPowerRemove offers the scripts that undo the setup on the PC.
func (f *Feature) showPowerRemove(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	if f.Setup == nil {
		return f.showPowerManual(c)
	}
	tok := f.Setup.TokenFor(d.ID, d.Name)
	text := "<b>" + c.T("wol_power_rm_title", "name", esc(d.Name)) + "</b>\n\n" +
		c.T("wol_power_rm_help",
			"win", esc(f.Setup.ScriptURL(tok, pcsetup.WindowsRemove)),
			"linux", esc(f.Setup.ScriptURL(tok, pcsetup.LinuxRemove)))
	kb := bot.NewKB().
		Row(bot.Btn(c.T("btn_script_win"), "wol:pwfile", d.ID, pcsetup.WindowsRemove), bot.Btn(c.T("btn_script_linux"), "wol:pwfile", d.ID, pcsetup.LinuxRemove)).
		Row(bot.Btn(c.T("btn_power_forget"), "wol:pwoff", d.ID)).
		Row(bot.Btn(c.T("btn_back"), "wol:pwr", d.ID))
	return c.Show(bot.Screen{Text: text, Keyboard: kb.Build()})
}

// sendScript uploads a script as a file for PCs where piping from the
// network is not an option.
func (f *Feature) sendScript(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil || f.Setup == nil {
		return nil
	}
	kind := c.Arg(1)
	body, err := f.Setup.Script(f.Setup.TokenFor(d.ID, d.Name), kind, true)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "owrt-bot-script")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, pcsetup.FileName(kind))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	c.Ack()
	caption := c.T("wol_script_caption_sh", "file", pcsetup.FileName(kind))
	if strings.HasSuffix(path, ".ps1") {
		caption = c.T("wol_script_caption_ps1", "file", pcsetup.FileName(kind))
	}
	if err := c.SendDocument(path, caption); err != nil {
		return err
	}
	// The file landed under the menu; put a fresh menu below it.
	return c.Reply(bot.Screen{Text: c.T("wol_script_sent"), Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_back"), "wol:dev", d.ID)).Build()})
}

// OnSetupReport is called by the script server when a PC finishes the
// setup or removal script.
func (f *Feature) OnSetupReport(req pcsetup.Request, rep pcsetup.Report) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := f.M.Store.Device(req.DeviceID)
	if err != nil {
		return
	}
	if rep.Removed {
		d.Power = nil
		if err := f.M.Store.UpdateDevice(d); err == nil {
			f.Out.Broadcast(ctx, f.T.T("wol_power_removed_pc", "name", esc(d.Name), "host", esc(rep.Host)))
		}
		return
	}
	d.Power = &wol.Power{User: rep.User, OS: wol.OS(rep.OS), Restricted: true}
	if err := f.M.Store.UpdateDevice(d); err != nil {
		return
	}
	msg := f.T.T("wol_power_auto_ok", "name", esc(d.Name), "host", esc(rep.Host))
	if err := f.M.CheckPower(ctx, d, rep.IP); err != nil {
		msg = f.T.T("wol_power_auto_fail", "name", esc(d.Name), "host", esc(rep.Host)) + " " + errText(f.T.T, err)
	}
	if mac, err := wol.NormalizeMAC(rep.MAC); err == nil && mac != d.MAC {
		msg += "\n" + f.T.T("wol_power_mac_differs", "pc", mac, "saved", d.MAC)
	}
	f.Out.Broadcast(ctx, msg)
}

func (f *Feature) showPowerManual(c *bot.Ctx) error {
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
	back := "wol:pwr"
	if f.Setup == nil {
		back = "wol:dev"
	}
	kb := bot.NewKB().
		Row(bot.Btn("🪟 Windows", "wol:pwos", d.ID, string(wol.Windows)), bot.Btn("🐧 Linux", "wol:pwos", d.ID, string(wol.Linux))).
		Row(bot.Btn(c.T("btn_show_key"), "wol:key", d.ID)).
		RowIf(d.Power != nil, bot.Btn(c.T("btn_power_disable"), "wol:pwoff", d.ID)).
		Row(bot.Btn(c.T("btn_back"), back, d.ID))
	return c.Show(bot.Screen{Text: text, Keyboard: kb.Build()})
}

func (f *Feature) askPowerUser(c *bot.Ctx) error {
	d, err := f.device(c, c.Arg(0))
	if err != nil {
		return nil
	}
	target := wol.OS(c.Arg(1))
	if target != wol.Windows && target != wol.Linux {
		return f.showPowerManual(c)
	}
	return c.Ask("wol_user", d.ID+":"+string(target), bot.Screen{
		Text:     c.T("wol_ask_user", "name", esc(d.Name)),
		Keyboard: cancelTo(c, "wol:pwman", d.ID),
	})
}

func (f *Feature) onPowerUser(c *bot.Ctx) error {
	id, target, _ := strings.Cut(c.Arg(0), ":")
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
	d.Power = &wol.Power{User: user, OS: wol.OS(target)}
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
