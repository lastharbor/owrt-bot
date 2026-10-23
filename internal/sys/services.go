package sys

import (
	"os"
	"path/filepath"
	"strings"
)

// Service is a daemon shown on the system screen.
type Service struct {
	Label   string // "Zapret"
	Process string // name in /proc/<pid>/comm, "nfqws2"
	Init    string // /etc/init.d/<Init>, "zapret2"
}

// ParseServices reads "Label:process:init,..." (init defaults to process).
func ParseServices(spec string) []Service {
	var out []Service
	for _, item := range strings.Split(spec, ",") {
		p := strings.Split(strings.TrimSpace(item), ":")
		if len(p) < 2 || p[0] == "" || p[1] == "" {
			continue
		}
		s := Service{Label: p[0], Process: p[1], Init: p[1]}
		if len(p) > 2 && p[2] != "" {
			s.Init = p[2]
		}
		out = append(out, s)
	}
	return out
}

// RunningProcesses returns the set of process names (comm) in procRoot.
func RunningProcesses(procRoot string) map[string]bool {
	out := map[string]bool{}
	matches, _ := filepath.Glob(filepath.Join(procRoot, "[0-9]*", "comm"))
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil {
			out[strings.TrimSpace(string(b))] = true
		}
	}
	return out
}
