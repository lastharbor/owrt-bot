//go:build linux

// Command screens renders the bot's real screens with demo data and
// writes them as JSON for README screenshots (tools/screens/shoot.mjs).
// It runs on Linux (statfs for the disk line); on an OpenWrt router:
//
//	GOOS=linux GOARCH=arm64 go build -o screens ./tools/screens
//	./screens -out screens.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/bottest"
	"github.com/lastharbor/owrt-bot/internal/features/admin"
	"github.com/lastharbor/owrt-bot/internal/features/clients"
	"github.com/lastharbor/owrt-bot/internal/features/network"
	"github.com/lastharbor/owrt-bot/internal/features/packages"
	"github.com/lastharbor/owrt-bot/internal/features/system"
	"github.com/lastharbor/owrt-bot/internal/features/wolui"
	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/pkgmgr"
	"github.com/lastharbor/owrt-bot/internal/sys"
	"github.com/lastharbor/owrt-bot/internal/telegram"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

const chat = 1

type shot struct {
	Name     string                   `json:"name"`
	Text     string                   `json:"text"`
	Keyboard *telegram.InlineKeyboard `json:"keyboard"`
	Mine     string                   `json:"mine,omitempty"` // a user message shown above
}

// ---- fakes: a small home LAN

type lan struct {
	mu     sync.Mutex
	online map[string]bool             // ip -> answers ping
	arp    map[string]net.HardwareAddr // ip -> answers ARP only
	wakeAt map[string]int              // ip -> checks until it comes up after a magic packet
	woken  map[string]bool
}

func (l *lan) Ping(_ context.Context, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.woken[ip] {
		if l.wakeAt[ip]--; l.wakeAt[ip] <= 0 {
			l.online[ip] = true
		}
	}
	return l.online[ip]
}

func (l *lan) Probe(_ context.Context, _ string, ip net.IP) (net.HardwareAddr, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.arp[ip.String()], nil
}

func (l *lan) Name() string { return "demo" }

func (l *lan) Send(_ context.Context, _ string, mac net.HardwareAddr) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, hw := range l.arp {
		if hw.String() == mac.String() {
			l.woken[ip] = true
		}
	}
	return nil
}

type demoPM struct{}

func (demoPM) Name() string                  { return "opkg" }
func (demoPM) Installed(context.Context) int { return 214 }
func (demoPM) ListsUpdated() time.Time       { return time.Now().Add(-2 * time.Hour) }
func (demoPM) Refresh(context.Context, func(string)) error {
	return nil
}
func (demoPM) Upgradable(context.Context) ([]pkgmgr.Upgrade, error) {
	return []pkgmgr.Upgrade{
		{Name: "luci-base", From: "26.278", To: "26.286"},
		{Name: "dnsmasq-full", From: "2.90-r3", To: "2.90-r4"},
		{Name: "kmod-nft-core", From: "6.6.110-1", To: "6.6.112-1"},
	}, nil
}
func (demoPM) Upgrade(context.Context, []string, func(string)) error { return nil }

type demoLog struct{}

func (demoLog) Tail(int) string { return "" }

type demoPrefs struct{}

func (demoPrefs) SetLanguage(string) error { return nil }

type noBlock struct{}

func (noBlock) Block(context.Context, string, string, int) error { return nil }
func (noBlock) Unblock(context.Context, string) error            { return nil }
func (noBlock) Blocked(context.Context) (map[string]string, error) {
	return map[string]string{"02:5e:10:aa:bb:07": "27m41s"}, nil
}

// fixedIP answers ifconfig.me with a documentation address.
type fixedIP struct{}

func (fixedIP) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("203.0.113.7")), Request: r}, nil
}

func write(root, rel, body string) {
	p := filepath.Join(root, rel)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(body), 0o644)
}

func main() {
	out := flag.String("out", "screens.json", "output file")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string) error {
	root, err := os.MkdirTemp("", "screens")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	write(root, "proc/loadavg", "0.42 0.35 0.30 1/180 4242\n")
	write(root, "proc/meminfo", "MemTotal: 2007640 kB\nMemFree: 714880 kB\nMemAvailable: 1185000 kB\n")
	write(root, "proc/uptime", "512345.12 900000.00\n")
	write(root, "sys/class/thermal/thermal_zone0/temp", "43750\n")
	write(root, "proc/net/dev", "Inter-|\n face |\n  eth0: 412345678901 0 0 0 0 0 0 0 23456789012 0 0 0 0 0 0 0\n")
	write(root, "tmp/dhcp.leases",
		"1 02:5e:10:aa:bb:01 192.168.1.10 gaming-pc *\n"+
			"1 02:5e:10:aa:bb:02 192.168.1.11 laptop *\n"+
			"1 02:5e:10:aa:bb:05 192.168.1.20 phone *\n"+
			"1 02:5e:10:aa:bb:07 192.168.1.23 kid-tablet *\n"+
			"1 02:5e:10:aa:bb:08 192.168.1.30 tv *\n")

	proc := sys.Proc{Root: root, DiskPath: "/"}
	metrics := &sys.Sampler{Proc: proc, WAN: "eth0"}
	var hist []sys.Sample
	for i := range 30 {
		x := float64(i)
		hist = append(hist, sys.Sample{
			CPU:    18 + 14*math.Sin(x/3) + 6*math.Sin(x*1.7),
			RxRate: (180 + 160*math.Sin(x/4) + 90*math.Abs(math.Sin(x*1.3))) * 1e6 / 8,
			TxRate: (14 + 9*math.Sin(x/2.5)) * 1e6 / 8,
		})
	}
	metrics.Seed(hist)

	mac := func(s string) net.HardwareAddr { hw, _ := net.ParseMAC(s); return hw }
	home := &lan{
		online: map[string]bool{"192.168.1.10": true},
		arp: map[string]net.HardwareAddr{
			"192.168.1.10": mac("02:5e:10:aa:bb:01"),
			"192.168.1.11": mac("02:5e:10:aa:bb:02"),
		},
		wakeAt: map[string]int{"192.168.1.11": 4},
		woken:  map[string]bool{},
	}

	store, err := wol.OpenJSON(filepath.Join(root, "wol.json"))
	if err != nil {
		return err
	}
	pc, _ := store.AddDevice(wol.Device{Name: "Игровой ПК", MAC: "02:5e:10:aa:bb:01", IP: "192.168.1.10", Notify: true,
		Power: &wol.Power{User: "user", OS: wol.Windows}})
	laptop, _ := store.AddDevice(wol.Device{Name: "Ноутбук", MAC: "02:5e:10:aa:bb:02", IP: "192.168.1.11", Notify: true})
	nas, _ := store.AddDevice(wol.Device{Name: "NAS", MAC: "02:5e:10:aa:bb:03", IP: "192.168.1.12"})
	group, _ := store.SaveGroup(wol.Group{Name: "Рабочее место", Devices: []string{pc.ID, nas.ID}})
	sched, _ := store.SaveSchedule(wol.Schedule{Target: wol.Target{Kind: wol.TargetGroup, ID: group.ID}, At: "08:30", Days: wol.Workdays, Enabled: true})
	_ = laptop

	presence := &wol.Presence{Ping: home, ARP: home}
	mgr := &wol.Manager{
		Store: store, Presence: presence, Watcher: &wol.Watcher{Repo: store, Presence: presence},
		Waker:       &wol.Waker{Senders: []wol.Sender{home}, Presence: presence, Poll: 3 * time.Second, Burst: 1},
		WaitTimeout: time.Minute,
	}
	tr := i18n.New("en").Add("ru", "Русский", i18n.RU).Add("en", "English", i18n.EN)
	tr.Use("ru")
	runner := sys.Exec{}
	router := bot.NewRouter()
	for _, f := range []bot.Feature{
		&system.Feature{Proc: proc, Metrics: metrics, Run: runner,
			Services: sys.ParseServices("dnsmasq,dropbear,odhcpd,uhttpd")},
		&wolui.Feature{M: mgr, T: tr, Discovery: wol.Discovery{Devices: store}},
		&network.Feature{Proc: proc, Metrics: metrics, WANIface: "eth0", PingHost: "1.1.1.1", Pinger: home,
			HTTP: &http.Client{Transport: fixedIP{}}},
		&clients.Feature{LeasesPath: filepath.Join(root, "tmp/dhcp.leases"), Blocker: noBlock{}, WOL: store},
		&packages.Feature{PM: demoPM{}},
		&admin.Backup{Run: runner, TmpDir: root},
		&admin.Logs{Log: demoLog{}},
		&admin.Language{T: tr, Prefs: demoPrefs{}},
	} {
		f.Register(router)
	}
	home.online["1.1.1.1"] = true

	msgr := bottest.New()
	b := bot.New(msgr, router, tr, slog.New(slog.NewTextHandler(io.Discard, nil)), bot.Options{AllowedChats: []int64{chat}})
	ctx := context.Background()
	var shots []shot
	take := func(name, mine string) {
		m := msgr.Last()
		if menu := menuID(msgr); menu != 0 {
			m = msgr.Get(menu)
		}
		shots = append(shots, shot{Name: name, Text: m.Text, Keyboard: m.Keyboard, Mine: mine})
	}
	press := func(data string) { b.Dispatch(ctx, bottest.Press(chat, menuID(msgr), data)) }

	b.Dispatch(ctx, bottest.Type(chat, "/start"))
	take("menu", "/start")
	press("sys:show")
	take("system", "")
	press("wol:list")
	take("wol", "")
	press("wol:dev:" + pc.ID)
	take("device", "")
	press("wol:grp:" + group.ID)
	take("group", "")
	press("wol:sc:" + sched.ID)
	take("schedule", "")
	press("net:show")
	take("network", "")
	press("cl:list")
	take("clients", "")
	press("pkg:show")
	take("packages", "")

	press("wol:wake:" + laptop.ID)
	waitFor(msgr, "Ноутбук</b>… 6")
	take("waking", "")
	waitFor(msgr, "в сети через")
	take("woke", "")

	raw, _ := json.MarshalIndent(shots, "", "  ")
	return os.WriteFile(out, raw, 0o644)
}

// menuID is the message the bot keeps editing.
func menuID(m *bottest.Messenger) int {
	if len(m.Sent) == 0 {
		return 0
	}
	return m.Sent[len(m.Sent)-1]
}

func waitFor(m *bottest.Messenger, substr string) {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if strings.Contains(m.Get(menuID(m)).Text, substr) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "timeout waiting for", substr)
}
