package sys

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseUCIShow(t *testing.T) {
	text := `dhcp.@host[1]=host
dhcp.@host[1].name='Pixel-7-Pro'
dhcp.@host[1].mac='32:87:cc:08:6c:f1'
dhcp.lan=dhcp
dhcp.lan.interface='lan'
`
	secs := ParseUCIShow("dhcp", text)
	if len(secs) != 2 {
		t.Fatalf("got %d sections", len(secs))
	}
	if secs[0].Name != "@host[1]" || secs[0].Type != "host" || secs[0].Options["mac"] != "32:87:cc:08:6c:f1" {
		t.Errorf("bad host section: %+v", secs[0])
	}
}

func TestLeases(t *testing.T) {
	p := filepath.Join(t.TempDir(), "leases")
	os.WriteFile(p, []byte("1792783618 02:5E:10:AA:BB:01 192.168.1.121 HomePC 01:60\n1 aa:bb:cc:dd:ee:ff 192.168.1.5 * x\n"), 0o600)
	l := Leases(p)
	if len(l) != 2 || l[0].MAC != "02:5e:10:aa:bb:01" || l[0].Hostname != "HomePC" || l[1].Hostname != "" {
		t.Errorf("leases = %+v", l)
	}
}

func TestSnapshotFromFixture(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o600)
	}
	write("proc/loadavg", "0.50 0.40 0.30 1/100 1\n")
	write("proc/meminfo", "MemTotal: 2000 kB\nMemFree: 500 kB\nMemAvailable: 1000 kB\n")
	write("proc/uptime", "3661.5 100\n")
	write("sys/class/thermal/thermal_zone0/temp", "41250\n")
	write("proc/net/dev", "Inter-|\n face |\n  eth0: 100 1 0 0 0 0 0 0 200 2 0 0 0 0 0 0\n")

	p := Proc{Root: root}
	s := p.Snapshot()
	if s.MemPercent() != 50 || s.TempC != 41.25 || s.Load1 != 0.5 || s.Uptime.Seconds() != 3661 {
		t.Errorf("snapshot = %+v", s)
	}
	rx, tx, ok := p.IfaceCounters("eth0")
	if !ok || rx != 100 || tx != 200 {
		t.Errorf("counters %d %d %v", rx, tx, ok)
	}
}

func TestParsePOSIXTZ(t *testing.T) {
	for in, want := range map[string]int{"MSK-3": 3 * 3600, "UTC0": 0, "EST5EDT,M3.2.0,M11.1.0": -5 * 3600, "<+0530>-5:30": 5*3600 + 1800} {
		loc, ok := ParsePOSIXTZ(in)
		if !ok {
			t.Errorf("%q not parsed", in)
			continue
		}
		if _, off := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone(); off != want {
			t.Errorf("%q offset %d want %d", in, off, want)
		}
	}
}
