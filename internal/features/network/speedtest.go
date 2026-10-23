package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultSpeedtestURLs are tried in order; the first one that starts
// sending data wins. Large files so parallel streams never run dry.
// Each stream re-requests until the window ends, so file size does not
// matter; Cloudflare refuses __down above ~25 MB.
const DefaultSpeedtestURLs = "https://speed.cloudflare.com/__down?bytes=25000000," +
	"https://proof.ovh.net/files/100Mb.dat," +
	"http://speedtest.selectel.ru/100MB"

// SpeedResult is a finished measurement.
type SpeedResult struct {
	Host    string
	Bytes   int64
	Elapsed time.Duration
}

func (r SpeedResult) Mbps() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Bytes) * 8 / 1e6 / r.Elapsed.Seconds()
}

// SpeedTest downloads with several parallel streams for a fixed time, the
// way browser speed tests do: one stream cannot fill a 1 Gbit/s link
// through TCP slow start and a single server's per-flow limits.
type SpeedTest struct {
	HTTP     *http.Client
	URLs     []string
	Streams  int           // default 4
	Duration time.Duration // measuring window after the first byte, default 8 s
	Connect  time.Duration // first byte must arrive within this, default 4 s
}

// Progress reports bytes received since the first byte, once a second.
type Progress func(host string, bytes int64, elapsed time.Duration)

// ParseURLs splits a comma-separated list.
func ParseURLs(spec string) []string {
	var out []string
	for _, u := range strings.Split(spec, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// Run tries each URL until one works. On total failure it returns one
// error per server so the user sees what was tried.
func (s SpeedTest) Run(ctx context.Context, progress Progress) (SpeedResult, error) {
	var errs []error
	for _, u := range s.URLs {
		r, err := s.runOne(ctx, u, progress)
		if err == nil {
			return r, nil
		}
		if ctx.Err() != nil {
			return SpeedResult{}, ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", hostOf(u), err))
	}
	if len(errs) == 0 {
		return SpeedResult{}, errors.New("no speedtest servers configured")
	}
	return SpeedResult{}, errors.Join(errs...)
}

var errNoData = errors.New("no data")

func (s SpeedTest) runOne(ctx context.Context, rawURL string, progress Progress) (SpeedResult, error) {
	streams, window, connect := s.Streams, s.Duration, s.Connect
	if streams <= 0 {
		streams = 4
	}
	if window <= 0 {
		window = 8 * time.Second
	}
	if connect <= 0 {
		connect = 4 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		total    atomic.Int64
		started  = make(chan struct{})
		once     sync.Once
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.stream(ctx, rawURL, &total, func() { once.Do(func() { close(started) }) })
			if err != nil && ctx.Err() == nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()

	select {
	case <-started:
	case <-allDone:
		if firstErr != nil {
			return SpeedResult{}, firstErr
		}
		return SpeedResult{}, errNoData
	case <-time.After(connect):
		cancel()
		<-allDone
		return SpeedResult{}, fmt.Errorf("no data within %s", connect)
	case <-ctx.Done():
		<-allDone
		return SpeedResult{}, ctx.Err()
	}

	// Count from the first byte, so connect/TLS time does not dilute it.
	base, start := total.Load(), time.Now()
	host := hostOf(rawURL)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	end := time.NewTimer(window)
	defer end.Stop()
loop:
	for {
		select {
		case <-tick.C:
			if progress != nil {
				progress(host, total.Load()-base, time.Since(start))
			}
		case <-end.C:
			break loop
		case <-allDone: // every stream gave up early: still a valid sample
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	elapsed := time.Since(start)
	bytes := total.Load() - base
	cancel()
	<-allDone
	if bytes <= 0 {
		return SpeedResult{}, errNoData
	}
	return SpeedResult{Host: host, Bytes: bytes, Elapsed: elapsed}, nil
}

// stream downloads rawURL over and over until ctx ends. An error after
// some data has flowed ends this stream quietly: what it got still counts.
func (s SpeedTest) stream(ctx context.Context, rawURL string, total *atomic.Int64, gotData func()) error {
	got := false
	for ctx.Err() == nil {
		n, err := s.fetch(ctx, rawURL, total, gotData)
		got = got || n > 0
		if err != nil {
			if got {
				return nil
			}
			return err
		}
	}
	return nil
}

func (s SpeedTest) fetch(ctx context.Context, rawURL string, total *atomic.Int64, gotData func()) (int64, error) {
	var read int64
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "owrt-bot speedtest")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			read += int64(n)
			total.Add(int64(n))
			gotData()
		}
		if err == io.EOF {
			return read, nil
		}
		if err != nil {
			return read, err
		}
	}
}
