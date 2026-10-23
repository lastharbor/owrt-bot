// Command owrt-bot is a Telegram bot for an OpenWrt router.
//
// This file is the composition root: the only place that knows concrete
// types. Everything below it depends on small interfaces.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/config"
	"github.com/lastharbor/owrt-bot/internal/features/admin"
	"github.com/lastharbor/owrt-bot/internal/features/alerts"
	"github.com/lastharbor/owrt-bot/internal/features/clients"
	"github.com/lastharbor/owrt-bot/internal/features/network"
	"github.com/lastharbor/owrt-bot/internal/features/system"
	"github.com/lastharbor/owrt-bot/internal/features/wifi"
	"github.com/lastharbor/owrt-bot/internal/features/wolui"
	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/logx"
	"github.com/lastharbor/owrt-bot/internal/netx"
	"github.com/lastharbor/owrt-bot/internal/prefs"
	"github.com/lastharbor/owrt-bot/internal/sys"
	"github.com/lastharbor/owrt-bot/internal/telegram"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

var version = "dev"

const defaultServices = "ZeroBlock:zeroblock,sing-box:sing-box,Zapret:nfqws2:zapret2,Tailscale:tailscaled:tailscale,TorrServer:TorrServer:torrserver"

func main() {
	cfgPath := flag.String("config", "/etc/owrt-bot/config.conf", "config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	probe := flag.Bool("probe", false, "check every saved device once, print the result and exit")
	wakeMAC := flag.String("wake", "", "send a magic packet to this MAC on the LAN and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	run := run
	switch {
	case *probe:
		run = probeOnce
	case *wakeMAC != "":
		run = func(cfgPath string) error { return wakeOnce(cfgPath, *wakeMAC) }
	}
	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "owrt-bot:", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	time.Local = sys.LocalZone("/etc/TZ")
	if err := os.MkdirAll(cfg.Paths.DataDir, 0o700); err != nil {
		return err
	}
	logFile, err := logx.OpenRotating(cfg.Paths.LogFile, int64(cfg.LogMaxKB)*1024)
	if err != nil {
		return err
	}
	log := logx.New(logFile, cfg.LogLevel)
	log.Info("starting", "version", version, "tz", time.Local.String())

	// ---- i18n and preferences
	tr := i18n.New("en").Add("ru", "Русский", i18n.RU).Add("en", "English", i18n.EN)
	pf, err := prefs.Open(filepath.Join(cfg.Paths.DataDir, "prefs.json"))
	if err != nil {
		return err
	}
	lang := cfg.Language
	if l := pf.Language(); l != "" {
		lang = l
	}
	tr.Use(lang)

	// ---- platform adapters
	runner := sys.Exec{}
	uci := sys.UCI{R: runner}
	proc := sys.Proc{Root: "", DiskPath: "/"}
	neigh := netx.NeighTable{R: runner}
	pinger := netx.ExecPinger{R: runner}

	// ---- wake-on-LAN domain
	store, err := wol.OpenJSON(filepath.Join(cfg.Paths.DataDir, "wol.json"))
	if err != nil {
		return err
	}
	if n := wol.ImportLegacy(store, cfg.LegacyWOL); n > 0 {
		log.Info("imported devices from legacy [wol] section", "count", n)
	}
	presence := &wol.Presence{
		Neigh: neigh, ARP: netx.ARPProber{}, Ping: pinger, TCP: netx.TCPAlive, LANIface: cfg.LANIface,
	}
	watcher := &wol.Watcher{Repo: store, Presence: presence, Interval: 30 * time.Second}
	mgr := &wol.Manager{
		Store:    store,
		Presence: presence,
		Waker:    &wol.Waker{Senders: []wol.Sender{netx.RawWOL{}, netx.UDPWOL{Port: 9}}, Presence: presence, LANIface: cfg.LANIface},
		Watcher:  watcher,
	}
	wolFeature := &wolui.Feature{
		M: mgr,
		Discovery: wol.Discovery{
			LeasesPath: "/tmp/dhcp.leases", UCI: uci, Neigh: neigh, Devices: store, LANIface: cfg.LANIface,
		},
		T: tr,
	}
	// Assign interfaces only on success: a nil *SSHPower inside a non-nil
	// interface would pass "!= nil" checks and crash later.
	if power, err := wol.NewSSHPower(filepath.Join(cfg.Paths.DataDir, "id_ed25519"), filepath.Join(cfg.Paths.DataDir, "known_hosts")); err != nil {
		log.Error("SSH key unavailable, power control disabled", "err", err)
	} else {
		mgr.Power = power
		wolFeature.Keys = power
	}

	// ---- telegram and features
	tg := telegram.New(cfg.Telegram.Token, telegram.Options{APIBase: cfg.Telegram.APIBase, DNSServer: cfg.DNSServer})
	httpc := &http.Client{Timeout: 30 * time.Second}
	wan, _ := uci.Get(context.Background(), "network.wan.device")
	if wan == "" {
		wan = "eth0"
	}
	spec := defaultServices
	if v := cfg.Raw("bot", "services"); v != "" {
		spec = v
	}
	var services []sys.Service // only those actually installed on this router
	for _, s := range sys.ParseServices(spec) {
		if _, err := os.Stat("/etc/init.d/" + s.Init); err == nil {
			services = append(services, s)
		}
	}
	metrics := &sys.Sampler{Proc: proc, WAN: wan, Every: 2 * time.Second}
	netFeature := &network.Feature{
		Proc: proc, Metrics: metrics, WANIface: wan, PingHost: cfg.Alerts.WANHost, Pinger: pinger, HTTP: httpc, SpeedtestURL: cfg.SpeedtestURL,
	}

	router := bot.NewRouter()
	features := []bot.Feature{
		&system.Feature{Proc: proc, Metrics: metrics, Run: runner, Services: services},
		wolFeature,
		netFeature,
		&clients.Feature{LeasesPath: "/tmp/dhcp.leases", Blocker: &clients.NFTBlocker{R: runner}, WOL: store},
		&admin.Packages{Run: runner, StatusFile: "/usr/lib/opkg/status"},
		&admin.Backup{Run: runner, TmpDir: os.TempDir()},
		&admin.Logs{Log: logFile},
		&admin.Language{T: tr, Prefs: pf},
	}
	if wf := (&wifi.Feature{UCI: uci, Run: runner}); wf.Available(context.Background()) {
		features = append(features, wf)
	}
	for _, f := range features {
		f.Register(router)
	}
	b := bot.New(tg, router, tr, log, bot.Options{
		AllowedChats: cfg.Telegram.AllowedChats,
		OffsetFile:   "/tmp/owrt-bot.offset",
		Middleware:   []bot.Middleware{bot.Recover(), bot.Timing(log)},
	})
	wolFeature.Out = b
	watcher.Subscribe(wolFeature.OnPresence)

	// ---- run
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go watcher.Run(ctx)
	go (&wol.Scheduler{Repo: store, Fire: wolFeature.OnSchedule, Log: log}).Run(ctx)
	go metrics.Run(ctx)
	go (&alerts.Monitor{Cfg: cfg.Alerts, Proc: proc, Ping: pinger, T: tr, Out: b, Log: log}).Run(ctx)
	go publishCommands(ctx, tg, router, tr, log)

	b.Run(ctx, tg)
	log.Info("stopped")
	return nil
}

func publishCommands(ctx context.Context, tg *telegram.Client, r *bot.Router, tr *i18n.Translator, log *slog.Logger) {
	var cmds []telegram.BotCommand
	for _, c := range r.Commands() {
		cmds = append(cmds, telegram.BotCommand{Command: c.Name, Description: tr.T(c.DescKey)})
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := tg.SetCommands(ctx, cmds); err != nil {
		log.Warn("setMyCommands failed", "err", err)
	}
}

// probeOnce is a diagnostic: same presence logic as the bot, no Telegram.
func probeOnce(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	store, err := wol.OpenJSON(filepath.Join(cfg.Paths.DataDir, "wol.json"))
	if err != nil {
		return err
	}
	wol.ImportLegacy(store, cfg.LegacyWOL)
	runner := sys.Exec{}
	p := &wol.Presence{
		Neigh: netx.NeighTable{R: runner}, ARP: netx.ARPProber{}, Ping: netx.ExecPinger{R: runner},
		TCP: netx.TCPAlive, LANIface: cfg.LANIface,
	}
	for _, d := range store.Devices() {
		start := time.Now()
		r := p.Check(context.Background(), d)
		hw, arpErr := netx.ARPProber{}.Probe(context.Background(), cfg.LANIface, net.ParseIP(r.IP))
		fmt.Printf("%-16s %s %-15s status=%d arp=%v arpErr=%v took=%s\n",
			d.Name, d.MAC, r.IP, r.Status, hw, arpErr, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// wakeOnce sends magic packets without starting the bot (cron, debugging).
func wakeOnce(cfgPath, mac string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	mac, err = wol.NormalizeMAC(mac)
	if err != nil {
		return err
	}
	w := &wol.Waker{Senders: []wol.Sender{netx.RawWOL{}, netx.UDPWOL{Port: 9}}, LANIface: cfg.LANIface}
	if err := w.Send(context.Background(), wol.Device{MAC: mac}); err != nil {
		return err
	}
	fmt.Println("sent to", mac, "via", cfg.LANIface)
	return nil
}
