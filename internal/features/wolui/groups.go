package wolui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

func (f *Feature) showGroups(c *bot.Ctx) error {
	groups := f.M.Store.Groups()
	lines := []string{"<b>" + c.T("wol_groups_title") + "</b>", ""}
	kb := bot.NewKB()
	if len(groups) == 0 {
		lines = append(lines, c.T("wol_groups_empty"))
	}
	for _, g := range groups {
		label := fmt.Sprintf("👥 %s (%d)", g.Name, len(g.Devices))
		kb.Row(bot.Btn(label, "wol:grp", g.ID), bot.Btn("⚡", "wol:gwake", g.ID))
	}
	kb.Row(bot.Btn(c.T("btn_group_new"), "wol:gnew")).
		Row(bot.Btn(c.T("btn_to_list"), "wol:list"))
	return c.Show(bot.Screen{Text: strings.Join(lines, "\n"), Keyboard: kb.Build()})
}

func (f *Feature) group(c *bot.Ctx, id string) (wol.Group, error) {
	g, err := f.M.Store.Group(id)
	if err != nil {
		c.Toast(c.T("wol_not_found"))
		_ = f.showGroups(c)
	}
	return g, err
}

// showGroup lists every device as a membership toggle.
func (f *Feature) showGroup(c *bot.Ctx) error {
	g, err := f.group(c, c.Arg(0))
	if err != nil {
		return nil
	}
	return c.Show(f.groupScreen(c, g))
}

func (f *Feature) groupScreen(c *bot.Ctx, g wol.Group) bot.Screen {
	kb := bot.NewKB()
	var btns []bot.Button
	for _, d := range f.M.Store.Devices() {
		mark := "▫️"
		if slices.Contains(g.Devices, d.ID) {
			mark = "✅"
		}
		btns = append(btns, bot.Btn(mark+" "+d.Name, "wol:gtog", g.ID, d.ID))
	}
	kb.Grid(2, btns...).
		Row(bot.Btn(c.T("btn_wake_group"), "wol:gwake", g.ID)).
		Row(bot.Btn(c.T("btn_rename"), "wol:gren", g.ID), bot.Btn(c.T("btn_delete"), "wol:gdel", g.ID)).
		Row(bot.Btn(c.T("btn_to_groups"), "wol:groups"))
	return bot.Screen{
		Text:     "<b>👥 " + esc(g.Name) + "</b>\n\n" + c.T("wol_group_hint"),
		Keyboard: kb.Build(),
	}
}

func (f *Feature) toggleMember(c *bot.Ctx) error {
	g, err := f.group(c, c.Arg(0))
	if err != nil {
		return nil
	}
	id := c.Arg(1)
	if i := slices.Index(g.Devices, id); i >= 0 {
		g.Devices = slices.Delete(g.Devices, i, i+1)
	} else if _, err := f.M.Store.Device(id); err == nil {
		g.Devices = append(g.Devices, id)
	}
	if g, err = f.M.Store.SaveGroup(g); err != nil {
		return err
	}
	return c.Show(f.groupScreen(c, g))
}

func (f *Feature) askGroupName(c *bot.Ctx) error {
	return c.Ask("wol_gname", "", bot.Screen{Text: c.T("wol_ask_group"), Keyboard: cancelTo(c, "wol:groups")})
}

func (f *Feature) askGroupRename(c *bot.Ctx) error {
	g, err := f.group(c, c.Arg(0))
	if err != nil {
		return nil
	}
	return c.Ask("wol_gname", g.ID, bot.Screen{Text: c.T("wol_ask_group"), Keyboard: cancelTo(c, "wol:grp", g.ID)})
}

// onGroupName creates a group (empty arg) or renames one.
func (f *Feature) onGroupName(c *bot.Ctx) error {
	name, err := wol.CleanName(c.Text)
	if err != nil {
		return c.Ask("wol_gname", c.Arg(0), bot.Screen{
			Text: errText(c.T, err) + "\n\n" + c.T("wol_ask_group"), Keyboard: cancelTo(c, "wol:groups"),
		})
	}
	g := wol.Group{Name: name}
	if id := c.Arg(0); id != "" {
		if g, err = f.group(c, id); err != nil {
			return nil
		}
		g.Name = name
	}
	if g, err = f.M.Store.SaveGroup(g); err != nil {
		return err
	}
	return c.Show(f.groupScreen(c, g))
}

func (f *Feature) confirmGroupDelete(c *bot.Ctx) error {
	g, err := f.group(c, c.Arg(0))
	if err != nil {
		return nil
	}
	return c.Show(bot.Screen{
		Text: c.T("wol_confirm_gdelete", "name", esc(g.Name)),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes_delete"), "wol:gdelok", g.ID), bot.Btn(c.T("btn_cancel"), "wol:grp", g.ID)).
			Build(),
	})
}

func (f *Feature) deleteGroup(c *bot.Ctx) error {
	if err := f.M.Store.DeleteGroup(c.Arg(0)); err != nil && err != wol.ErrNotFound {
		return err
	}
	c.Toast(c.T("wol_group_deleted"))
	return f.showGroups(c)
}
