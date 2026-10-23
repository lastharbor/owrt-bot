package wol

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/lastharbor/owrt-bot/internal/netx"
)

// Status is what the UI shows next to a device.
type Status int

const (
	StatusUnknown Status = iota // no IP known, cannot test
	StatusOffline               // nothing answers at all
	StatusAsleep                // only the NIC answers ARP (sleep with ARP offload, or firewall drops ping)
	StatusOnline                // the OS answers ping/TCP
)

func (s Status) Emoji() string {
	switch s {
	case StatusOnline:
		return "🟢"
	case StatusAsleep:
		return "🟡"
	case StatusOffline:
		return "🔴"
	}
	return "⚪"
}

// Result is one presence observation.
type Result struct {
	Status Status
	IP     string // the address actually tested (may differ from Device.IP after DHCP changes)
	At     time.Time
}

// Collaborators, each a one-method interface so tests can fake them.
type (
	Neighbors interface {
		Neighbors(ctx context.Context) ([]netx.Neighbor, error)
	}
	ARPProber interface {
		Probe(ctx context.Context, iface string, ip net.IP) (net.HardwareAddr, error)
	}
	Pinger interface {
		Ping(ctx context.Context, ip string) bool
	}
	TCPChecker func(ctx context.Context, ip string, port int, timeout time.Duration) bool
)

// Presence decides whether a device is up. It also caches the latest
// result per device so list screens render instantly from the watcher's
// observations instead of probing on every tap.
type Presence struct {
	Neigh    Neighbors
	ARP      ARPProber
	Ping     Pinger
	TCP      TCPChecker
	LANIface string
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]Result
}

// Classify turns raw probe answers into a Status.
//
//   - osAlive: ping or TCP got an answer from the operating system
//   - arpMAC:  who answered ARP for the IP ("" = nobody)
//   - wantMAC: the device's own MAC
func Classify(osAlive bool, arpMAC, wantMAC string) Status {
	switch {
	case osAlive:
		return StatusOnline
	case arpMAC != "" && strings.EqualFold(arpMAC, wantMAC):
		return StatusAsleep
	default:
		// Nobody answered, or a different machine now owns the IP.
		return StatusOffline
	}
}

func (p *Presence) iface(d Device) string {
	if d.Iface != "" {
		return d.Iface
	}
	return p.LANIface
}

// resolveIP prefers the neighbour table's current IP for the MAC, since
// DHCP may have moved the device since it was saved.
func (p *Presence) resolveIP(ctx context.Context, d Device) string {
	if p.Neigh == nil {
		return d.IP
	}
	rows, err := p.Neigh.Neighbors(ctx)
	if err != nil {
		return d.IP
	}
	for _, n := range rows {
		if n.MAC != "" && strings.EqualFold(n.MAC, d.MAC) {
			return n.IP
		}
	}
	return d.IP
}

// Check probes one device now (up to ~2 s when it is down).
func (p *Presence) Check(ctx context.Context, d Device) Result {
	r := Result{IP: p.resolveIP(ctx, d), At: p.now()}
	if r.IP == "" {
		r.Status = StatusUnknown
		p.store(d.ID, r)
		return r
	}
	alive := p.Ping != nil && p.Ping.Ping(ctx, r.IP)
	if !alive && d.Power != nil && p.TCP != nil {
		alive = p.TCP(ctx, r.IP, d.Power.PortOrDefault(), time.Second)
	}
	var arpMAC string
	if !alive && p.ARP != nil {
		if hw, err := p.ARP.Probe(ctx, p.iface(d), net.ParseIP(r.IP)); err == nil && hw != nil {
			arpMAC = hw.String()
		}
	}
	r.Status = Classify(alive, arpMAC, d.MAC)
	p.store(d.ID, r)
	return r
}

// CheckAll probes devices concurrently.
func (p *Presence) CheckAll(ctx context.Context, devs []Device) map[string]Result {
	out := make(map[string]Result, len(devs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range devs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := p.Check(ctx, d)
			mu.Lock()
			out[d.ID] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// Cached returns the last observation if it is younger than maxAge.
func (p *Presence) Cached(id string, maxAge time.Duration) (Result, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.cache[id]
	if !ok || p.now().Sub(r.At) > maxAge {
		return Result{}, false
	}
	return r, true
}

// Snapshot returns cached results where fresh and probes the rest.
func (p *Presence) Snapshot(ctx context.Context, devs []Device, maxAge time.Duration) map[string]Result {
	out := map[string]Result{}
	var stale []Device
	for _, d := range devs {
		if r, ok := p.Cached(d.ID, maxAge); ok {
			out[d.ID] = r
		} else {
			stale = append(stale, d)
		}
	}
	for id, r := range p.CheckAll(ctx, stale) {
		out[id] = r
	}
	return out
}

func (p *Presence) store(id string, r Result) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = map[string]Result{}
	}
	p.cache[id] = r
}

func (p *Presence) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
