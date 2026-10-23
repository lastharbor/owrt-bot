package netx

import (
	"context"
	"strings"
)

// NeighState is the kernel NUD state of a neighbour entry.
type NeighState string

const (
	NeighReachable  NeighState = "REACHABLE"
	NeighStale      NeighState = "STALE"
	NeighDelay      NeighState = "DELAY"
	NeighProbe      NeighState = "PROBE"
	NeighFailed     NeighState = "FAILED"
	NeighIncomplete NeighState = "INCOMPLETE"
	NeighPermanent  NeighState = "PERMANENT"
)

// Fresh reports states proving the host answered very recently.
func (s NeighState) Fresh() bool {
	return s == NeighReachable || s == NeighDelay || s == NeighProbe
}

// Neighbor is one row of `ip neigh show`.
type Neighbor struct {
	IP    string
	Dev   string
	MAC   string // lowercase, "" for FAILED/INCOMPLETE
	State NeighState
}

// ParseNeigh parses `ip neigh show` (iproute2 and BusyBox print the same
// shape): "192.168.1.121 dev br-lan lladdr 02:5e:.. ref 1 used 0/0/0 probes 1 REACHABLE".
func ParseNeigh(text string) []Neighbor {
	var out []Neighbor
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.Contains(f[0], ":") { // skip IPv6
			continue
		}
		n := Neighbor{IP: f[0], State: NeighState(f[len(f)-1])}
		for i := 1; i+1 < len(f); i++ {
			switch f[i] {
			case "dev":
				n.Dev = f[i+1]
			case "lladdr":
				n.MAC = strings.ToLower(f[i+1])
			}
		}
		out = append(out, n)
	}
	return out
}

// runner is the subset of sys.Runner this package needs; declared here to
// avoid an import cycle and keep netx dependency-free.
type runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NeighTable reads the kernel neighbour table via `ip neigh show`.
type NeighTable struct{ R runner }

func (t NeighTable) Neighbors(ctx context.Context) ([]Neighbor, error) {
	out, err := t.R.Run(ctx, "ip", "neigh", "show")
	if err != nil {
		return nil, err
	}
	return ParseNeigh(string(out)), nil
}
