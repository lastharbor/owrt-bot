// Package network shows WAN throughput, reachability and runs a speed test.
package network

import (
	"context"
	"errors"
	"fmt"
	"html"
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
	HTTP  *http.Client
	Speed SpeedTest

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

// speedtest runs in the background with a live progress screen, so the
// chat stays usable for the ~10 s it takes.
func (f *Feature) speedtest(c *bot.Ctx) error {
	c.Ack()
	if err := c.Show(f.speedScreen(c, "", nil, 0)); err != nil {
		return err
	}
	c.Go(func(bg *bot.Ctx) {
		var rates []float64
		var last int64
		r, err := f.Speed.Run(bg, func(host string, bytes int64, el time.Duration) {
			rates = append(rates, float64(bytes-last)) // bytes in the last second
			last = bytes
			_ = bg.Show(f.speedScreen(bg, host, rates, el))
		})
		var line string
		if err != nil {
			line = bg.T("net_speed_fail") + "\n<code>" + html.EscapeString(err.Error()) + "</code>"
		} else {
			line = bg.T("net_speed_result", "mbps", fmt.Sprintf("%.0f", r.Mbps()), "host", r.Host,
				"mb", r.Bytes>>20, "sec", fmt.Sprintf("%.1f", r.Elapsed.Seconds()))
			if len(rates) > 1 {
				line += "\n<code>" + ui.Spark(rates, 0, 0) + "</code>"
			}
		}
		if err := f.liveWith(bg, line); errors.Is(err, bot.ErrStale) {
			_ = bg.Notify(line)
		}
	})
	return nil
}

func (f *Feature) speedScreen(c *bot.Ctx, host string, rates []float64, el time.Duration) bot.Screen {
	text := c.T("net_speed_running")
	if len(rates) > 0 {
		text = c.T("net_speed_progress", "host", host, "sec", int(el.Seconds()),
			"now", ui.Rate(c.T, rates[len(rates)-1])) + "\n<code>" + ui.Spark(rates, 0, 0) + "</code>"
	}
	return bot.Screen{Text: text, Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_back"), "net:show")).Build()}
}
