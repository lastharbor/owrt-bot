package sys

import (
	"context"
	"encoding/json"
	"strings"
)

// Service is a daemon shown on the system screen, named by its init
// script (/etc/init.d/<Name>).
type Service struct {
	Name string
}

// ParseServices reads a comma-separated list of init script names. The
// older "Label:process:init" form is still accepted: its init part wins.
func ParseServices(spec string) []Service {
	var out []Service
	for _, item := range strings.Split(spec, ",") {
		p := strings.Split(strings.TrimSpace(item), ":")
		name := p[len(p)-1]
		if len(p) == 2 {
			name = p[1] // "Label:process" meant init == process
		}
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, Service{Name: name})
		}
	}
	return out
}

// ServiceStates asks procd which services have a running instance:
// `ubus call service list` returns {"name": {"instances": {"x": {"running": true}}}}.
// Services procd does not manage are absent from the map.
func ServiceStates(ctx context.Context, r Runner) map[string]bool {
	out, err := r.Run(ctx, "ubus", "call", "service", "list")
	if err != nil {
		return nil
	}
	var list map[string]struct {
		Instances map[string]struct {
			Running bool `json:"running"`
		} `json:"instances"`
	}
	if json.Unmarshal(out, &list) != nil {
		return nil
	}
	states := make(map[string]bool, len(list))
	for name, svc := range list {
		running := false
		for _, inst := range svc.Instances {
			running = running || inst.Running
		}
		states[name] = running
	}
	return states
}
