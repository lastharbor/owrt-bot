package sys

import (
	"context"
	"os"
	"strings"
)

// UCI wraps the uci CLI. Values are passed as single argv entries, so no
// quoting is needed even for "@wifi-iface[0]".
type UCI struct{ R Runner }

func (u UCI) Get(ctx context.Context, path string) (string, error) {
	out, err := u.R.Run(ctx, "uci", "-q", "get", path)
	return strings.TrimSpace(string(out)), err
}

func (u UCI) Set(ctx context.Context, path, value string) error {
	_, err := u.R.Run(ctx, "uci", "set", path+"="+value)
	return err
}

func (u UCI) Commit(ctx context.Context, config string) error {
	_, err := u.R.Run(ctx, "uci", "commit", config)
	return err
}

// Section is one uci section with its options.
type Section struct {
	Name    string // "cfg0a1b2c" or "@host[1]"-style anonymous id as uci prints it
	Type    string
	Options map[string]string
}

// Show returns the sections of one config in file order.
func (u UCI) Show(ctx context.Context, config string) ([]Section, error) {
	out, err := u.R.Run(ctx, "uci", "-q", "show", config)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return ParseUCIShow(config, string(out)), nil
}

// ParseUCIShow parses `uci show <config>` output:
//
//	dhcp.@host[0]=host
//	dhcp.@host[0].mac='AA:BB:...'
func ParseUCIShow(config, text string) []Section {
	var order []string
	byName := map[string]*Section{}
	prefix := config + "."
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		key, val, ok := strings.Cut(line[len(prefix):], "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, "'")
		sec, opt, hasOpt := strings.Cut(key, ".")
		s, exists := byName[sec]
		if !exists {
			s = &Section{Name: sec, Options: map[string]string{}}
			byName[sec] = s
			order = append(order, sec)
		}
		if hasOpt {
			s.Options[opt] = val
		} else {
			s.Type = val
		}
	}
	out := make([]Section, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

// Lease is a DHCP lease from dnsmasq.
type Lease struct {
	MAC, IP, Hostname string
}

// Leases reads dnsmasq's lease file ("<expiry> <mac> <ip> <host> <id>").
func Leases(path string) []Lease {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Lease
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		host := f[3]
		if host == "*" {
			host = ""
		}
		out = append(out, Lease{MAC: strings.ToLower(f[1]), IP: f[2], Hostname: host})
	}
	return out
}
