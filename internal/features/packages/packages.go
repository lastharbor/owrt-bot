// Package packages shows installed/upgradable packages and runs
// "update lists" and upgrades with live output.
package packages

import (
	"context"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/pkgmgr"
)

type Feature struct {
	PM pkgmgr.Manager

	busy sync.Mutex // one package operation at a time, like opkg's own lock
}

const sessionList = "packages.list"

// maxButtons keeps the keyboard usable when many packages are outdated.
const maxButtons = 12

func (f *Feature) Register(r *bot.Router) {
	r.Menu(60, "btn_packages", "pkg:show")
	r.Callback("pkg:show", func(c *bot.Ctx) error { return f.show(c, "") })
	r.Callback("pkg:refresh", f.refresh)
	r.Callback("pkg:all", f.confirmAll)
	r.Callback("pkg:one", f.confirmOne)
	r.Callback("pkg:go", f.upgrade)
}

// split separates packages that can be upgraded one by one from kernel
// modules, which are listed but not offered.
func split(list []pkgmgr.Upgrade) (normal, kernel []pkgmgr.Upgrade) {
	for _, u := range list {
		if u.Kernel() {
			kernel = append(kernel, u)
		} else {
			normal = append(normal, u)
		}
	}
	return normal, kernel
}

func (f *Feature) screen(c *bot.Ctx, extra string) bot.Screen {
	list, err := f.PM.Upgradable(c)
	normal, kernel := split(list)
	c.Session().Set(sessionList, normal)

	lines := []string{
		"<b>" + c.T("pkg_title", "pm", f.PM.Name()) + "</b>", "",
		c.T("pkg_count", "n", f.PM.Installed(c)),
	}
	if t := f.PM.ListsUpdated(); t.IsZero() {
		lines = append(lines, c.T("pkg_lists_unknown"))
	} else {
		lines = append(lines, c.T("pkg_lists", "when", t.Format("02.01 15:04")))
		if time.Since(t) > 24*time.Hour {
			lines = append(lines, c.T("pkg_lists_old"))
		}
	}
	lines = append(lines, "")
	switch {
	case err != nil:
		lines = append(lines, c.T("pkg_failed")+" <code>"+html.EscapeString(err.Error())+"</code>")
	case len(list) == 0:
		lines = append(lines, c.T("pkg_none"))
	default:
		lines = append(lines, c.T("pkg_upg_header", "n", len(list)))
		for i, u := range normal {
			if i == 30 {
				lines = append(lines, "…")
				break
			}
			lines = append(lines, fmt.Sprintf("• <b>%s</b> %s → %s", html.EscapeString(u.Name), html.EscapeString(u.From), html.EscapeString(u.To)))
		}
		for _, u := range kernel {
			lines = append(lines, "• ⚠️ "+html.EscapeString(u.Name))
		}
		if len(kernel) > 0 {
			lines = append(lines, "", c.T("pkg_kmod_hint"))
		}
	}
	if extra != "" {
		lines = append(lines, "", extra)
	}

	kb := bot.NewKB().Row(bot.Btn(c.T("btn_pkg_refresh"), "pkg:refresh"))
	if len(normal) > 0 {
		kb.Row(bot.Btn(c.T("btn_pkg_all", "n", len(normal)), "pkg:all"))
		var btns []bot.Button
		for i, u := range normal {
			if i == maxButtons {
				break
			}
			btns = append(btns, bot.Btn("⬆️ "+u.Name, "pkg:one", fmt.Sprint(i)))
		}
		kb.Grid(2, btns...)
	}
	kb.Row(bot.Btn(c.T("btn_back"), "menu:main"))
	return bot.Screen{Text: strings.Join(lines, "\n"), Keyboard: kb.Build()}
}

func (f *Feature) show(c *bot.Ctx, extra string) error { return c.Show(f.screen(c, extra)) }

func (f *Feature) stored(c *bot.Ctx) []pkgmgr.Upgrade {
	list, _ := c.Session().Get(sessionList).([]pkgmgr.Upgrade)
	return list
}

func (f *Feature) confirmAll(c *bot.Ctx) error {
	list := f.stored(c)
	if len(list) == 0 {
		return f.show(c, "")
	}
	return c.Show(bot.Screen{
		Text: c.T("pkg_confirm_all", "n", len(list)),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes"), "pkg:go", "all"), bot.Btn(c.T("btn_cancel"), "pkg:show")).Build(),
	})
}

func (f *Feature) confirmOne(c *bot.Ctx) error {
	list := f.stored(c)
	var i int
	if _, err := fmt.Sscan(c.Arg(0), &i); err != nil || i < 0 || i >= len(list) {
		return f.show(c, "")
	}
	u := list[i]
	return c.Show(bot.Screen{
		Text: c.T("pkg_confirm_one", "name", html.EscapeString(u.Name), "from", html.EscapeString(u.From), "to", html.EscapeString(u.To)),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes"), "pkg:go", c.Arg(0)), bot.Btn(c.T("btn_cancel"), "pkg:show")).Build(),
	})
}

func (f *Feature) refresh(c *bot.Ctx) error {
	return f.run(c, f.PM.Name()+" update", 3*time.Minute, func(ctx context.Context, line func(string)) error {
		return f.PM.Refresh(ctx, line)
	})
}

func (f *Feature) upgrade(c *bot.Ctx) error {
	list := f.stored(c)
	var names []string
	if c.Arg(0) == "all" {
		for _, u := range list {
			names = append(names, u.Name)
		}
	} else {
		var i int
		if _, err := fmt.Sscan(c.Arg(0), &i); err == nil && i >= 0 && i < len(list) {
			names = []string{list[i].Name}
		}
	}
	if len(names) == 0 {
		return f.show(c, "")
	}
	return f.run(c, f.PM.Name()+" upgrade", 15*time.Minute, func(ctx context.Context, line func(string)) error {
		return f.PM.Upgrade(ctx, names, line)
	})
}

// run executes a package operation in the background, showing its output
// live (at most one edit a second), then returns to the package list.
func (f *Feature) run(c *bot.Ctx, title string, limit time.Duration, op func(context.Context, func(string)) error) error {
	if !f.busy.TryLock() {
		c.Toast(c.T("pkg_busy"))
		return nil
	}
	c.Ack()
	start := time.Now()
	if err := c.Show(progress(c, title, start, nil)); err != nil {
		f.busy.Unlock()
		return err
	}
	c.Go(func(bg *bot.Ctx) {
		defer f.busy.Unlock()
		var (
			mu    sync.Mutex
			tail  []string
			shown time.Time
		)
		ctx, cancel := context.WithTimeout(bg, limit)
		defer cancel()
		err := op(ctx, func(l string) {
			mu.Lock()
			tail = append(tail, l)
			if len(tail) > 8 {
				tail = tail[len(tail)-8:]
			}
			snap := append([]string(nil), tail...)
			due := time.Since(shown) >= time.Second
			if due {
				shown = time.Now()
			}
			mu.Unlock()
			if due {
				_ = bg.Show(progress(bg, title, start, snap))
			}
		})
		took := time.Since(start).Round(time.Second)
		result := bg.T("pkg_done", "sec", int(took.Seconds()))
		if err != nil {
			mu.Lock()
			last := strings.Join(tail, "\n")
			mu.Unlock()
			result = bg.T("pkg_failed") + " <code>" + html.EscapeString(err.Error()) + "</code>"
			if last != "" {
				result += "\n<pre>" + html.EscapeString(last) + "</pre>"
			}
		}
		if bg.Stale() {
			_ = bg.Notify(title + ": " + result)
			return
		}
		_ = bg.Show(f.screen(bg, result))
	})
	return nil
}

func progress(c *bot.Ctx, title string, start time.Time, tail []string) bot.Screen {
	text := c.T("pkg_running", "cmd", title, "sec", int(time.Since(start).Seconds()))
	if len(tail) > 0 {
		text += "\n<pre>" + html.EscapeString(strings.Join(tail, "\n")) + "</pre>"
	}
	return bot.Screen{Text: text, Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_back"), "pkg:show")).Build()}
}
