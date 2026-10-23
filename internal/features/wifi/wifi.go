// Package wifi toggles wireless interfaces and changes SSID/password.
// main registers it only when the router actually has wifi-iface sections.
package wifi

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/sys"
)

type Feature struct {
	UCI sys.UCI
	Run sys.Runner
}

type iface struct {
	Section  string
	SSID     string
	Disabled bool
}

// Available reports whether any wifi-iface is configured.
func (f *Feature) Available(ctx context.Context) bool {
	return len(f.ifaces(ctx)) > 0
}

func (f *Feature) Register(r *bot.Router) {
	r.Menu(50, "btn_wifi", "wifi:list")
	r.Callback("wifi:list", f.list)
	r.Callback("wifi:show", f.show)
	r.Callback("wifi:toggle", f.toggle)
	r.Callback("wifi:ssid", f.askSSID)
	r.Callback("wifi:pass", f.askPass)
	r.Prompt("wifi_ssid", f.onSSID)
	r.Prompt("wifi_pass", f.onPass)
}

func (f *Feature) ifaces(ctx context.Context) []iface {
	secs, err := f.UCI.Show(ctx, "wireless")
	if err != nil {
		return nil
	}
	var out []iface
	for _, s := range secs {
		if s.Type == "wifi-iface" {
			out = append(out, iface{Section: s.Name, SSID: s.Options["ssid"], Disabled: s.Options["disabled"] == "1"})
		}
	}
	return out
}

// Section names like "@wifi-iface[0]" are not callback-safe, so buttons
// carry the index into the current list instead.
func (f *Feature) byIndex(c *bot.Ctx, arg string) (iface, bool) {
	list := f.ifaces(c)
	for i, it := range list {
		if arg == strconv.Itoa(i) {
			return it, true
		}
	}
	return iface{}, false
}

func mark(it iface) string {
	if it.Disabled {
		return "🔴"
	}
	return "🟢"
}

func (f *Feature) list(c *bot.Ctx) error {
	kb := bot.NewKB()
	for i, it := range f.ifaces(c) {
		kb.Row(bot.Btn(mark(it)+" "+it.SSID, "wifi:show", strconv.Itoa(i)))
	}
	kb.Row(bot.Btn(c.T("btn_back"), "menu:main"))
	return c.Show(bot.Screen{Text: "<b>" + c.T("wifi_title") + "</b>", Keyboard: kb.Build()})
}

func (f *Feature) card(c *bot.Ctx, idx, extra string) error {
	it, ok := f.byIndex(c, idx)
	if !ok {
		return f.list(c)
	}
	toggle := c.T("btn_wifi_off")
	if it.Disabled {
		toggle = c.T("btn_wifi_on")
	}
	text := mark(it) + " <b>" + it.SSID + "</b>"
	if extra != "" {
		text += "\n\n" + extra
	}
	return c.Show(bot.Screen{Text: text, Keyboard: bot.NewKB().
		Row(bot.Btn(toggle, "wifi:toggle", idx)).
		Row(bot.Btn(c.T("btn_wifi_ssid"), "wifi:ssid", idx), bot.Btn(c.T("btn_wifi_pass"), "wifi:pass", idx)).
		Row(bot.Btn(c.T("btn_back"), "wifi:list")).Build()})
}

func (f *Feature) show(c *bot.Ctx) error { return f.card(c, c.Arg(0), "") }

func (f *Feature) apply(ctx context.Context, section, opt, val string) error {
	if err := f.UCI.Set(ctx, "wireless."+section+"."+opt, val); err != nil {
		return err
	}
	if err := f.UCI.Commit(ctx, "wireless"); err != nil {
		return err
	}
	_, err := f.Run.Run(ctx, "wifi", "reload")
	return err
}

func (f *Feature) toggle(c *bot.Ctx) error {
	it, ok := f.byIndex(c, c.Arg(0))
	if !ok {
		return f.list(c)
	}
	val := "1"
	if it.Disabled {
		val = "0"
	}
	c.Ack()
	if err := f.apply(c, it.Section, "disabled", val); err != nil {
		return err
	}
	return f.card(c, c.Arg(0), c.T("wifi_applied"))
}

func (f *Feature) askSSID(c *bot.Ctx) error {
	return c.Ask("wifi_ssid", c.Arg(0), bot.Screen{Text: c.T("wifi_ask_ssid"),
		Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_cancel"), "wifi:show", c.Arg(0))).Build()})
}

func (f *Feature) askPass(c *bot.Ctx) error {
	return c.Ask("wifi_pass", c.Arg(0), bot.Screen{Text: c.T("wifi_ask_pass"),
		Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_cancel"), "wifi:show", c.Arg(0))).Build()})
}

func (f *Feature) onSSID(c *bot.Ctx) error {
	ssid := strings.TrimSpace(c.Text)
	if ssid == "" || len(ssid) > 32 {
		return c.Ask("wifi_ssid", c.Arg(0), bot.Screen{Text: c.T("wifi_bad_ssid") + "\n\n" + c.T("wifi_ask_ssid")})
	}
	return f.set(c, "ssid", ssid)
}

func (f *Feature) onPass(c *bot.Ctx) error {
	n := utf8.RuneCountInString(c.Text)
	if n < 8 || n > 63 {
		return c.Ask("wifi_pass", c.Arg(0), bot.Screen{Text: c.T("wifi_bad_pass") + "\n\n" + c.T("wifi_ask_pass")})
	}
	return f.set(c, "key", c.Text)
}

func (f *Feature) set(c *bot.Ctx, opt, val string) error {
	it, ok := f.byIndex(c, c.Arg(0))
	if !ok {
		return f.list(c)
	}
	if err := f.apply(c, it.Section, opt, val); err != nil {
		return err
	}
	return f.card(c, c.Arg(0), c.T("wifi_applied"))
}
