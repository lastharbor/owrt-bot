package wol

import (
	"context"
	"sort"
	"strings"

	"github.com/lastharbor/owrt-bot/internal/sys"
)

// Candidate is a LAN device that could be added for waking.
type Candidate struct {
	MAC    string
	IP     string
	Name   string
	Source string // "static", "dhcp", "arp"
}

// StaticHosts lists DHCP static reservations (uci dhcp @host).
type StaticHosts interface {
	Show(ctx context.Context, config string) ([]sys.Section, error)
}

// Discovery merges every source of "what is on the LAN".
type Discovery struct {
	LeasesPath string
	UCI        StaticHosts
	Neigh      Neighbors
	Devices    DeviceRepo
	LANIface   string // only neighbours on this interface are offered
}

// Candidates returns devices not yet in the repo, named where possible,
// sorted with named ones first.
func (d Discovery) Candidates(ctx context.Context) []Candidate {
	byMAC := map[string]*Candidate{}
	add := func(mac, ip, name, src string) {
		mac, err := NormalizeMAC(mac)
		if err != nil {
			return
		}
		c, ok := byMAC[mac]
		if !ok {
			c = &Candidate{MAC: mac, Source: src}
			byMAC[mac] = c
		}
		if c.IP == "" {
			c.IP = ip
		}
		if c.Name == "" && name != "" {
			c.Name = name
		}
	}
	// Order matters: static names beat DHCP hostnames beat nothing.
	if d.UCI != nil {
		if secs, err := d.UCI.Show(ctx, "dhcp"); err == nil {
			for _, s := range secs {
				if s.Type == "host" {
					for _, mac := range strings.Fields(s.Options["mac"]) {
						add(mac, s.Options["ip"], s.Options["name"], "static")
					}
				}
			}
		}
	}
	for _, l := range sys.Leases(d.LeasesPath) {
		add(l.MAC, l.IP, l.Hostname, "dhcp")
	}
	if d.Neigh != nil {
		if rows, err := d.Neigh.Neighbors(ctx); err == nil {
			for _, n := range rows {
				if n.MAC != "" && n.Dev == d.LANIface {
					add(n.MAC, n.IP, "", "arp")
				}
			}
		}
	}
	var out []Candidate
	for mac, c := range byMAC {
		if _, err := d.Devices.DeviceByMAC(mac); err == nil {
			continue
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Name == "") != (out[j].Name == "") {
			return out[i].Name != ""
		}
		if out[i].Name != out[j].Name {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].IP < out[j].IP
	})
	return out
}
