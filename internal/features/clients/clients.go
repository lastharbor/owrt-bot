// Package clients lists LAN clients and blocks them temporarily.
package clients

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/sys"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

// Blocker is the policy for cutting a client off (strategy; nftables in
// production).
type Blocker interface {
	Block(ctx context.Context, mac, ip string, minutes int) error
	Unblock(ctx context.Context, mac string) error
	Blocked(ctx context.Context) (map[string]string, error) // mac -> "expires 29m"
}

// NFTBlocker drops forwarded traffic from MACs in a timeout set; the
// kernel expires entries itself, so nothing has to run to unblock.
type NFTBlocker struct {
	R    sys.Runner
	once sync.Once
	err  error
}

const nftTable = "owrt_bot"

const nftScript = `table inet owrt_bot {
	set blocked {
		type ether_addr
		flags timeout
	}
	chain forward {
		type filter hook forward priority filter - 5; policy accept;
		ether saddr @blocked drop
	}
}
`

func (b *NFTBlocker) ensure(ctx context.Context) error {
	b.once.Do(func() {
		if _, err := b.R.Run(ctx, "nft", "list", "table", "inet", nftTable); err == nil {
			return
		}
		f, err := os.CreateTemp("", "owrt-bot-*.nft")
		if err != nil {
			b.err = err
			return
		}
		defer os.Remove(f.Name())
		f.WriteString(nftScript)
		f.Close()
		_, b.err = b.R.Run(ctx, "nft", "-f", f.Name())
	})
	return b.err
}

func (b *NFTBlocker) Block(ctx context.Context, mac, ip string, minutes int) error {
	if err := b.ensure(ctx); err != nil {
		return err
	}
	_ = b.Unblock(ctx, mac) // re-adding with a new timeout needs delete first
	if _, err := b.R.Run(ctx, "nft", "add", "element", "inet", nftTable, "blocked",
		fmt.Sprintf("{ %s timeout %dm }", mac, minutes)); err != nil {
		return err
	}
	// Flow offloading keeps established connections alive past the
	// forward hook; dropping their conntrack entries cuts them now.
	if ip != "" {
		_, _ = b.R.Run(ctx, "conntrack", "-D", "-s", ip)
		_, _ = b.R.Run(ctx, "conntrack", "-D", "-d", ip)
	}
	return nil
}

func (b *NFTBlocker) Unblock(ctx context.Context, mac string) error {
	if err := b.ensure(ctx); err != nil {
		return err
	}
	_, err := b.R.Run(ctx, "nft", "delete", "element", "inet", nftTable, "blocked", "{ "+mac+" }")
	return err
}

var elemRe = regexp.MustCompile(`([0-9a-f]{2}(?::[0-9a-f]{2}){5})(?:\s+timeout\s+\S+)?(?:\s+expires\s+(\S+?))?[,\s}]`)

func (b *NFTBlocker) Blocked(ctx context.Context) (map[string]string, error) {
	if err := b.ensure(ctx); err != nil {
		return nil, err
	}
	out, err := b.R.Run(ctx, "nft", "list", "set", "inet", nftTable, "blocked")
	if err != nil {
		return nil, err
	}
	return parseElements(string(out)), nil
}

func parseElements(text string) map[string]string {
	res := map[string]string{}
	_, after, ok := strings.Cut(text, "elements = {")
	if !ok {
		return res
	}
	for _, m := range elemRe.FindAllStringSubmatch(strings.ToLower(after), -1) {
		res[m[1]] = m[2]
	}
	return res
}

// Feature is the clients screen.
type Feature struct {
	LeasesPath string
	Blocker    Blocker
	WOL        wol.DeviceRepo
}

type client struct{ MAC, IP, Name string }

func (f *Feature) Register(r *bot.Router) {
	r.Menu(40, "btn_clients", "cl:list")
	r.Callback("cl:list", f.list)
	r.Callback("cl:show", f.show)
	r.Callback("cl:block", f.block)
	r.Callback("cl:unblock", f.unblock)
	r.Callback("cl:towol", f.toWOL)
}

func (f *Feature) clients() []client {
	var out []client
	for _, l := range sys.Leases(f.LeasesPath) {
		name := l.Hostname
		if name == "" {
			name = l.IP
		}
		out = append(out, client{MAC: l.MAC, IP: l.IP, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (f *Feature) find(mac string) client {
	for _, c := range f.clients() {
		if c.MAC == mac {
			return c
		}
	}
	return client{MAC: mac, Name: mac}
}

var live = bot.LiveOptions{Every: 5 * time.Second}

func (f *Feature) list(c *bot.Ctx) error { return c.Live(f.listScreen, live) }

func (f *Feature) listScreen(c *bot.Ctx) bot.Screen {
	blocked, _ := f.Blocker.Blocked(c)
	lines := []string{"<b>" + c.T("cl_title") + "</b>", ""}
	kb := bot.NewKB()
	cls := f.clients()
	if len(cls) == 0 {
		lines = append(lines, c.T("cl_none"))
	}
	for _, cl := range cls {
		mark := "•"
		if _, ok := blocked[cl.MAC]; ok {
			mark = "🚫"
		}
		lines = append(lines, fmt.Sprintf("%s %s (%s)", mark, cl.Name, cl.IP))
		kb.Row(bot.Btn(mark+" "+cl.Name, "cl:show", bot.CompactMAC(cl.MAC)))
	}
	kb.Row(bot.Btn(c.T("btn_refresh"), "cl:list"), bot.Btn(c.T("btn_back"), "menu:main"))
	return bot.Screen{Text: strings.Join(lines, "\n"), Keyboard: kb.Build()}
}

// card stays live so the block countdown ticks down on screen.
func (f *Feature) card(c *bot.Ctx, mac, extra string) error {
	return c.Live(func(c *bot.Ctx) bot.Screen { return f.cardScreen(c, mac, extra) }, live)
}

func (f *Feature) cardScreen(c *bot.Ctx, mac, extra string) bot.Screen {
	cl := f.find(mac)
	blocked, _ := f.Blocker.Blocked(c)
	exp, isBlocked := blocked[mac]
	status := c.T("cl_ok")
	if isBlocked {
		status = c.T("cl_blocked", "left", exp)
	}
	_, err := f.WOL.DeviceByMAC(mac)
	inWOL := err == nil
	text := fmt.Sprintf("<b>%s</b>\nIP: <code>%s</code>\nMAC: <code>%s</code>\n%s", cl.Name, cl.IP, mac, status)
	if inWOL {
		text += "\n" + c.T("cl_in_wol")
	}
	if extra != "" {
		text += "\n\n" + extra
	}
	cm := bot.CompactMAC(mac)
	kb := bot.NewKB().
		Row(bot.Btn(c.T("btn_block_n", "n", 5), "cl:block", cm, "5"),
			bot.Btn(c.T("btn_block_n", "n", 30), "cl:block", cm, "30"),
			bot.Btn(c.T("btn_block_n", "n", 120), "cl:block", cm, "120")).
		RowIf(isBlocked, bot.Btn(c.T("btn_unblock"), "cl:unblock", cm)).
		RowIf(!inWOL, bot.Btn(c.T("btn_to_wol"), "cl:towol", cm)).
		Row(bot.Btn(c.T("btn_back"), "cl:list"))
	return bot.Screen{Text: text, Keyboard: kb.Build()}
}

func (f *Feature) show(c *bot.Ctx) error { return f.card(c, bot.ExpandMAC(c.Arg(0)), "") }

func (f *Feature) block(c *bot.Ctx) error {
	mac := bot.ExpandMAC(c.Arg(0))
	var minutes int
	if _, err := fmt.Sscan(c.Arg(1), &minutes); err != nil || minutes <= 0 || minutes > 24*60 {
		return f.card(c, mac, "")
	}
	cl := f.find(mac)
	if err := f.Blocker.Block(c, mac, cl.IP, minutes); err != nil {
		return f.card(c, mac, c.T("cl_block_fail")+" <code>"+err.Error()+"</code>")
	}
	c.Toast(c.T("cl_block_done", "name", cl.Name, "n", minutes))
	return f.card(c, mac, "")
}

func (f *Feature) unblock(c *bot.Ctx) error {
	mac := bot.ExpandMAC(c.Arg(0))
	if err := f.Blocker.Unblock(c, mac); err != nil {
		c.Log().Warn("unblock", "err", err)
	}
	c.Toast(c.T("cl_unblock_done"))
	return f.card(c, mac, "")
}

func (f *Feature) toWOL(c *bot.Ctx) error {
	mac := bot.ExpandMAC(c.Arg(0))
	cl := f.find(mac)
	name, err := wol.CleanName(cl.Name)
	if err != nil {
		name = mac
	}
	if _, err := f.WOL.AddDevice(wol.Device{Name: name, MAC: mac, IP: cl.IP, Notify: true}); err != nil {
		c.Toast(c.T("wol_err_dup"))
	} else {
		c.Toast(c.T("wol_added", "name", name))
	}
	return f.card(c, mac, "")
}
