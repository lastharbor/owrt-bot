package sys

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Snapshot is a point-in-time view of router health.
type Snapshot struct {
	Load1, Load5, Load15    float64
	CPUs                    int
	MemUsedKB, MemTotalKB   uint64
	DiskUsedKB, DiskTotalKB uint64
	TempC                   float64 // 0 when unknown
	Uptime                  time.Duration
}

func (s Snapshot) MemPercent() float64  { return pct(s.MemUsedKB, s.MemTotalKB) }
func (s Snapshot) DiskPercent() float64 { return pct(s.DiskUsedKB, s.DiskTotalKB) }

// CPUPercent maps load1 onto all cores: load == cores is 100 %.
func (s Snapshot) CPUPercent() float64 {
	if s.CPUs == 0 {
		return 0
	}
	return s.Load1 / float64(s.CPUs) * 100
}

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// Proc reads health data from procfs/sysfs. Root is "/" in production
// and a fixture directory in tests.
type Proc struct {
	Root     string
	DiskPath string // filesystem to report, "/" on this router (eMMC root)
}

func (p Proc) path(rel string) string { return strings.TrimSuffix(p.Root, "/") + rel }

func (p Proc) read(rel string) string {
	b, _ := os.ReadFile(p.path(rel))
	return string(b)
}

// Snapshot gathers everything in one go; missing sources leave zeros.
func (p Proc) Snapshot() Snapshot {
	s := Snapshot{CPUs: runtime.NumCPU()}
	if f := strings.Fields(p.read("/proc/loadavg")); len(f) >= 3 {
		s.Load1, _ = strconv.ParseFloat(f[0], 64)
		s.Load5, _ = strconv.ParseFloat(f[1], 64)
		s.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
	mem := parseMeminfo(p.read("/proc/meminfo"))
	s.MemTotalKB = mem["MemTotal"]
	avail, ok := mem["MemAvailable"]
	if !ok {
		avail = mem["MemFree"]
	}
	if s.MemTotalKB > avail {
		s.MemUsedKB = s.MemTotalKB - avail
	}
	if f := strings.Fields(p.read("/proc/uptime")); len(f) > 0 {
		sec, _ := strconv.ParseFloat(f[0], 64)
		s.Uptime = time.Duration(sec) * time.Second
	}
	s.TempC = p.temp()
	s.DiskUsedKB, s.DiskTotalKB = p.disk()
	return s
}

func parseMeminfo(text string) map[string]uint64 {
	out := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err == nil {
			out[k] = n
		}
	}
	return out
}

func (p Proc) temp() float64 {
	for _, rel := range []string{
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/thermal/thermal_zone1/temp",
	} {
		raw := strings.TrimSpace(p.read(rel))
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v != 0 {
			if v > 1000 {
				v /= 1000
			}
			return v
		}
	}
	return 0
}

func (p Proc) disk() (used, total uint64) {
	path := p.DiskPath
	if path == "" {
		path = "/"
	}
	used, total, _ = diskUsage(p.path(path))
	return used, total
}

// IfaceCounters returns rx/tx byte counters for one interface.
func (p Proc) IfaceCounters(iface string) (rx, tx uint64, ok bool) {
	sc := bufio.NewScanner(strings.NewReader(p.read("/proc/net/dev")))
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found || strings.TrimSpace(name) != iface {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			return 0, 0, false
		}
		rx, _ = strconv.ParseUint(f[0], 10, 64)
		tx, _ = strconv.ParseUint(f[8], 10, 64)
		return rx, tx, true
	}
	return 0, 0, false
}
