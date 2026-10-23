// Package network shows WAN throughput, reachability and runs a speed test.
package network

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/sys"
	"github.com/lastharbor/owrt-bot/internal/ui"
)

// Feature renders WAN throughput from the shared sampler, plus
// reachability, and runs a speed test.
type Feature struct {
	Proc     sys.Proc
	Metrics  *sys.Sampler
	WANIface string
	PingHost string
	Pinger   interface {
		Ping(ctx context.Context, ip string) bool
	}
	HTTP         *http.Client
	SpeedtestURL string

	mu   sync.Mutex
	ip   string
	ipAt time.Time
}

var live = bot.LiveOptions{Every: 3 * time.Second}

func (f *Feature) Register(r *bot.Router) {
	r.Menu(30, "btn_network", "net:show")
	r.Callback("net:show", f.show)
	r.Callback("net:speed", f.speedtest)
}

func gb(b uint64) string { return fmt.Sprintf("%.2f GB", float64(b)/(1<<30)) }

func (f *Feature) publicIP(ctx context.Context) string {
	f.mu.Lock()
	cached, fresh := f.ip, f.ip != "" && time.Since(f.ipAt) < 5*time.Minute
	f.mu.Unlock()
	if fresh {
		return cached
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://ifconfig.me/ip", nil)
	req.Header.Set("User-Agent", "curl/8")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return cached
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip := strings.TrimSpace(string(b))
	f.mu.Lock()
	f.ip, f.ipAt = ip, time.Now()
	f.mu.Unlock()
	return ip
}

func (f *Feature) screen(c *bot.Ctx, extra string) bot.Screen {
	var (
		wg     sync.WaitGroup
		pingOK bool
		ip     string
	)
	wg.Add(2)
	go func() { defer wg.Done(); pingOK = f.Pinger.Ping(c, f.PingHost) }()
	go func() { defer wg.Done(); ip = f.publicIP(c) }()
	wg.Wait()

	hist := f.Metrics.History(30)
	rxs, txs := make([]float64, len(hist)), make([]float64, len(hist))
	for i, h := range hist {
		rxs[i], txs[i] = h.RxRate, h.TxRate
	}
	cur := f.Metrics.Latest()
	totRx, totTx, _ := f.Proc.IfaceCounters(f.WANIface)
	ping := c.T("net_ping_fail", "host", f.PingHost)
	if pingOK {
		ping = c.T("net_ping_ok", "host", f.PingHost)
	}
	if ip == "" {
		ip = "?"
	}
	lines := []string{
		"<b>" + c.T("net_title") + "</b> (" + f.WANIface + ")", "",
		c.T("net_rx", "v", ui.Rate(c.T, cur.RxRate)),
		"<code>" + ui.Spark(rxs, 0, 0) + "</code>",
		c.T("net_tx", "v", ui.Rate(c.T, cur.TxRate)),
		"<code>" + ui.Spark(txs, 0, 0) + "</code>",
		c.T("net_total", "rx", gb(totRx), "tx", gb(totTx)),
		ping,
		c.T("net_ip", "ip", ip),
	}
	if extra != "" {
		lines = append(lines, "", extra)
	}
	return bot.Screen{
		Text: strings.Join(lines, "\n"),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_refresh"), "net:show"), bot.Btn(c.T("btn_speedtest"), "net:speed")).
			Row(bot.Btn(c.T("btn_back"), "menu:main")).Build(),
	}
}

func (f *Feature) show(c *bot.Ctx) error { return f.liveWith(c, "") }

func (f *Feature) liveWith(c *bot.Ctx, extra string) error {
	return c.Live(func(c *bot.Ctx) bot.Screen { return f.screen(c, extra) }, live)
}

func (f *Feature) speedtest(c *bot.Ctx) error {
	c.Ack()
	if err := c.Show(bot.Screen{Text: c.T("net_speed_running")}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c, 20*time.Second)
	defer cancel()
	start := time.Now()
	var n int64
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.SpeedtestURL, nil)
	resp, err := f.HTTP.Do(req)
	if err == nil {
		n, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	el := time.Since(start).Seconds()
	res := c.T("error_generic")
	if n > 0 && el > 0 {
		res = c.T("net_speed_result", "mbps", fmt.Sprintf("%.0f", float64(n)*8/1e6/el), "mb", n>>20, "sec", fmt.Sprintf("%.1f", el))
	}
	return f.liveWith(c, res)
}
