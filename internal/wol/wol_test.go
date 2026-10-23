package wol

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/lastharbor/owrt-bot/internal/netx"
)

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"02:5E:10:AA:BB:01": "02:5e:10:aa:bb:01",
		"02-5e-10-aa-bb-01": "02:5e:10:aa:bb:01",
		"025e10aabb01":      "02:5e:10:aa:bb:01",
		"025e.10aa.bb01":    "02:5e:10:aa:bb:01",
	} {
		if got, err := NormalizeMAC(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "60:cf", "zz:cf:84:dd:7e:1b", "ff:ff:ff:ff:ff:ff", "01:00:5e:00:00:01"} {
		if _, err := NormalizeMAC(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestStoreCRUDAndCascade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wol.json")
	s, err := OpenJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := s.AddDevice(Device{Name: "PC", MAC: "02:5e:10:aa:bb:01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDevice(Device{Name: "dup", MAC: "02:5E:10:AA:BB:01"}); err != ErrDuplicate {
		t.Fatalf("dup err = %v", err)
	}
	g, _ := s.SaveGroup(Group{Name: "all", Devices: []string{pc.ID}})
	s.SaveSchedule(Schedule{Target: Target{TargetDevice, pc.ID}, At: "07:30", Days: Workdays, Enabled: true})
	s.SaveSchedule(Schedule{Target: Target{TargetGroup, g.ID}, At: "08:00", Days: Workdays, Enabled: true})

	// Reopen: data must survive.
	s2, err := OpenJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Devices()) != 1 || len(s2.Schedules()) != 2 {
		t.Fatalf("reload lost data: %+v %+v", s2.Devices(), s2.Schedules())
	}
	if err := s2.DeleteDevice(pc.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s2.Group(g.ID); len(got.Devices) != 0 {
		t.Errorf("device left in group: %+v", got)
	}
	if sc := s2.Schedules(); len(sc) != 1 || sc[0].Target.Kind != TargetGroup {
		t.Errorf("device schedule not cascaded: %+v", sc)
	}

	// Event log
	s2.AddEvent(LogEntry{DeviceID: "d1", Device: "PC", Kind: EventOnline})
	s2.AddEvent(LogEntry{DeviceID: "d1", Device: "PC", Kind: EventOffline})
	if len(s2.Events()) != 2 || len(s2.DeviceEvents("d1")) != 2 {
		t.Errorf("events: %+v", s2.Events())
	}
	s2.ClearEvents()
	if len(s2.Events()) != 0 {
		t.Errorf("clear failed: %+v", s2.Events())
	}
}

func TestImportLegacy(t *testing.T) {
	s, _ := OpenJSON(filepath.Join(t.TempDir(), "wol.json"))
	n := ImportLegacy(s, map[string]string{
		"pc1": "HomePC|02:5e:10:aa:bb:01|br-lan|192.168.1.121",
		"pc2": "broken",
	})
	if n != 1 {
		t.Fatalf("imported %d", n)
	}
	d := s.Devices()[0]
	if d.Name != "HomePC" || d.IP != "192.168.1.121" || d.Iface != "br-lan" {
		t.Errorf("device = %+v", d)
	}
	if ImportLegacy(s, map[string]string{"pc1": "X|11:22:33:44:55:66"}) != 0 {
		t.Error("import must not run on a non-empty store")
	}
}

func TestClassify(t *testing.T) {
	mac := "02:5e:10:aa:bb:01"
	cases := []struct {
		alive bool
		arp   string
		want  Status
	}{
		{true, "", StatusOnline},
		{false, mac, StatusAsleep},
		{false, "aa:aa:aa:aa:aa:aa", StatusOffline},
		{false, "", StatusOffline},
	}
	for _, c := range cases {
		if got := Classify(c.alive, c.arp, mac); got != c.want {
			t.Errorf("Classify(%v,%q) = %v want %v", c.alive, c.arp, got, c.want)
		}
	}
}

type fakeNeigh []netx.Neighbor

func (f fakeNeigh) Neighbors(context.Context) ([]netx.Neighbor, error) { return f, nil }

type fakePing map[string]bool

func (f fakePing) Ping(_ context.Context, ip string) bool { return f[ip] }

type noARP struct{}

func (noARP) Probe(context.Context, string, net.IP) (net.HardwareAddr, error) { return nil, nil }

func TestPresenceFollowsDHCPMove(t *testing.T) {
	p := &Presence{
		Neigh: fakeNeigh{{IP: "192.168.1.200", MAC: "02:5e:10:aa:bb:01", State: netx.NeighReachable}},
		Ping:  fakePing{"192.168.1.200": true},
		ARP:   noARP{},
	}
	r := p.Check(context.Background(), Device{ID: "d1", MAC: "02:5e:10:aa:bb:01", IP: "192.168.1.121"})
	if r.Status != StatusOnline || r.IP != "192.168.1.200" {
		t.Fatalf("r = %+v", r)
	}
	if _, ok := p.Cached("d1", time.Minute); !ok {
		t.Fatal("result not cached")
	}
}

func TestWatcherNeedsTwoObservations(t *testing.T) {
	s, _ := OpenJSON(filepath.Join(t.TempDir(), "wol.json"))
	d, _ := s.AddDevice(Device{Name: "PC", MAC: "02:5e:10:aa:bb:01", IP: "10.0.0.2", Notify: true})
	ping := fakePing{}
	w := &Watcher{Repo: s, Presence: &Presence{Ping: ping, ARP: noARP{}}}
	var events []Event
	w.Subscribe(func(e Event) { events = append(events, e) })
	ctx := context.Background()

	w.Tick(ctx) // baseline offline
	ping["10.0.0.2"] = true
	w.Tick(ctx) // first online sighting: candidate only
	if len(events) != 0 {
		t.Fatalf("reported after one observation: %+v", events)
	}
	w.Tick(ctx) // confirmed
	if len(events) != 1 || events[0].To != StatusOnline || events[0].Device.ID != d.ID {
		t.Fatalf("events = %+v", events)
	}
	w.Assume(d.ID, StatusOffline)
	ping["10.0.0.2"] = false
	w.Tick(ctx)
	w.Tick(ctx)
	if len(events) != 1 {
		t.Fatalf("assumed change was reported: %+v", events)
	}
}

func TestDueAndNext(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	s := Schedule{At: "07:30", Days: Workdays, Enabled: true}
	fri := time.Date(2026, 10, 23, 7, 35, 0, 0, loc) // Friday
	if !Due(s, fri) {
		t.Error("should fire 5 min late")
	}
	if Due(s, fri.Add(-10*time.Minute)) {
		t.Error("fired early")
	}
	if Due(s, fri.Add(20*time.Minute)) {
		t.Error("fired past catch-up window")
	}
	s.LastRun = "2026-10-23"
	if Due(s, fri) {
		t.Error("fired twice a day")
	}
	sat := time.Date(2026, 10, 24, 7, 30, 0, 0, loc)
	if Due(Schedule{At: "07:30", Days: Workdays, Enabled: true}, sat) {
		t.Error("fired on Saturday")
	}
	next := Next(Schedule{At: "07:30", Days: Workdays, Enabled: true}, fri)
	if next.Weekday() != time.Monday || next.Hour() != 7 {
		t.Errorf("next = %v", next)
	}
}

func TestWeekdays(t *testing.T) {
	if !Workdays.Has(time.Monday) || Workdays.Has(time.Sunday) || !AllWeek.Has(time.Sunday) {
		t.Error("bit mapping wrong")
	}
	if Workdays.Toggle(time.Saturday).Has(time.Saturday) != true {
		t.Error("toggle failed")
	}
}
