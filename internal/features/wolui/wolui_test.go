package wolui

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/bottest"
	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/wol"
)

const chat = 42

// fakePC turns "online" after it receives a magic packet.
type fakePC struct {
	mu    sync.Mutex
	awake bool
	sends int
}

func (p *fakePC) Name() string { return "fake" }
func (p *fakePC) Send(context.Context, string, net.HardwareAddr) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends++
	p.awake = true
	return nil
}
func (p *fakePC) Ping(context.Context, string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.awake
}

type noARP struct{}

func (noARP) Probe(context.Context, string, net.IP) (net.HardwareAddr, error) { return nil, nil }

type nopOut struct{}

func (nopOut) Broadcast(context.Context, string) {}
func (nopOut) BroadcastTemp(context.Context, string, time.Duration) {}

type env struct {
	b     *bot.Bot
	msgr  *bottest.Messenger
	store *wol.JSONStore
	pc    *fakePC
	menu  int
}

func setup(t *testing.T) *env {
	t.Helper()
	store, err := wol.OpenJSON(filepath.Join(t.TempDir(), "wol.json"))
	if err != nil {
		t.Fatal(err)
	}
	pc := &fakePC{}
	presence := &wol.Presence{Ping: pc, ARP: noARP{}}
	watcher := &wol.Watcher{Repo: store, Presence: presence}
	mgr := &wol.Manager{
		Store: store, Presence: presence, Watcher: watcher, WaitTimeout: time.Second,
		Waker: &wol.Waker{Senders: []wol.Sender{pc}, Presence: presence, Poll: 5 * time.Millisecond, Burst: 1},
	}
	tr := i18n.New("en").Add("en", "English", i18n.EN)
	f := &Feature{M: mgr, T: tr, Out: nopOut{}, Discovery: wol.Discovery{Devices: store}}
	r := bot.NewRouter()
	f.Register(r)
	msgr := bottest.New()
	b := bot.New(msgr, r, tr, slog.New(slog.NewTextHandler(io.Discard, nil)), bot.Options{AllowedChats: []int64{chat}, LiveMax: 30 * time.Millisecond})
	e := &env{b: b, msgr: msgr, store: store, pc: pc}
	e.b.Dispatch(context.Background(), bottest.Type(chat, "/wol"))
	e.menu = msgr.Last().ID
	return e
}

func (e *env) press(data string) bottest.Msg {
	e.b.Dispatch(context.Background(), bottest.Press(chat, e.menu, data))
	return e.msgr.Get(e.menu)
}

func (e *env) typ(text string) bottest.Msg {
	e.b.Dispatch(context.Background(), bottest.Type(chat, text))
	return e.msgr.Get(e.menu)
}

func TestManualAddRenameDelete(t *testing.T) {
	e := setup(t)
	e.press("wol:addm")
	m := e.typ("Gaming PC 02-5E-10-AA-BB-01 192.168.1.121")
	devs := e.store.Devices()
	if len(devs) != 1 || devs[0].Name != "Gaming PC" || devs[0].MAC != "02:5e:10:aa:bb:01" || devs[0].IP != "192.168.1.121" {
		t.Fatalf("devices = %+v", devs)
	}
	if !strings.Contains(m.Text, "Gaming PC") {
		t.Errorf("card not shown: %q", m.Text)
	}
	id := devs[0].ID

	// Bad MAC re-asks instead of failing silently.
	e.press("wol:mac:" + id)
	if m := e.typ("not-a-mac"); !strings.Contains(m.Text, "not a MAC") {
		t.Errorf("no validation message: %q", m.Text)
	}
	e.typ("12:22:33:44:55:66")
	if d, _ := e.store.Device(id); d.MAC != "12:22:33:44:55:66" {
		t.Errorf("MAC not updated: %+v", d)
	}

	e.press("wol:ren:" + id)
	e.typ("Office")
	if d, _ := e.store.Device(id); d.Name != "Office" {
		t.Errorf("rename failed: %+v", d)
	}

	// Delete asks first.
	m = e.press("wol:del:" + id)
	if _, ok := bottest.Buttons(m.Keyboard)["wol:delok:"+id]; !ok {
		t.Fatalf("no confirm button: %v", bottest.Buttons(m.Keyboard))
	}
	if len(e.store.Devices()) != 1 {
		t.Fatal("deleted before confirmation")
	}
	e.press("wol:delok:" + id)
	if len(e.store.Devices()) != 0 {
		t.Fatal("not deleted")
	}
}

func TestWakeShowsResultOnCard(t *testing.T) {
	e := setup(t)
	d, _ := e.store.AddDevice(wol.Device{Name: "PC", MAC: "02:5e:10:aa:bb:01", IP: "10.0.0.2"})
	e.press("wol:wake:" + d.ID)
	e.b.Wait()
	m := e.msgr.Get(e.menu)
	if !strings.Contains(m.Text, "online after") {
		t.Fatalf("final screen = %q", m.Text)
	}
	if e.pc.sends == 0 {
		t.Fatal("no magic packet sent")
	}
	if _, ok := bottest.Buttons(m.Keyboard)["wol:wake:"+d.ID]; !ok {
		t.Error("card keyboard missing after wake")
	}
}

func TestWakeResultBecomesMessageWhenUserLeft(t *testing.T) {
	e := setup(t)
	d, _ := e.store.AddDevice(wol.Device{Name: "PC", MAC: "02:5e:10:aa:bb:01", IP: "10.0.0.2"})
	// Slow the device down so the user can navigate away first.
	e.pc.mu.Lock()
	e.pc.awake = false
	e.pc.mu.Unlock()
	slow := &slowSender{inner: e.pc, delay: 50 * time.Millisecond}
	e.b = rebuildWithSender(t, e, slow)

	e.press("wol:wake:" + d.ID)
	e.press("wol:groups") // user leaves the progress screen
	e.b.Wait()
	if got := e.msgr.Get(e.menu).Text; !strings.Contains(got, "Groups") {
		t.Fatalf("progress overwrote the screen the user went to: %q", got)
	}
	if last := e.msgr.Last(); !strings.Contains(last.Text, "online after") {
		t.Fatalf("result not delivered separately: %q", last.Text)
	}
}

type slowSender struct {
	inner *fakePC
	delay time.Duration
}

func (s *slowSender) Name() string { return "slow" }
func (s *slowSender) Send(ctx context.Context, iface string, mac net.HardwareAddr) error {
	go func() {
		time.Sleep(s.delay)
		s.inner.Send(ctx, iface, mac)
	}()
	return nil
}

func rebuildWithSender(t *testing.T, e *env, s wol.Sender) *bot.Bot {
	presence := &wol.Presence{Ping: e.pc, ARP: noARP{}}
	mgr := &wol.Manager{
		Store: e.store, Presence: presence, Watcher: &wol.Watcher{Repo: e.store, Presence: presence}, WaitTimeout: time.Second,
		Waker: &wol.Waker{Senders: []wol.Sender{s}, Presence: presence, Poll: 20 * time.Millisecond, Burst: 1},
	}
	tr := i18n.New("en").Add("en", "English", i18n.EN)
	f := &Feature{M: mgr, T: tr, Out: nopOut{}, Discovery: wol.Discovery{Devices: e.store}}
	r := bot.NewRouter()
	f.Register(r)
	b := bot.New(e.msgr, r, tr, slog.New(slog.NewTextHandler(io.Discard, nil)), bot.Options{AllowedChats: []int64{chat}, LiveMax: 30 * time.Millisecond})
	b.Dispatch(context.Background(), bottest.Type(chat, "/wol"))
	e.menu = e.msgr.Last().ID
	return b
}

func TestGroupsAndSchedule(t *testing.T) {
	e := setup(t)
	d, _ := e.store.AddDevice(wol.Device{Name: "PC", MAC: "02:5e:10:aa:bb:01", IP: "10.0.0.2"})
	e.press("wol:gnew")
	e.typ("Home")
	g := e.store.Groups()
	if len(g) != 1 || g[0].Name != "Home" {
		t.Fatalf("groups = %+v", g)
	}
	e.press("wol:gtog:" + g[0].ID + ":" + d.ID)
	if g, _ := e.store.Group(g[0].ID); len(g.Devices) != 1 {
		t.Fatalf("member not added: %+v", g)
	}

	e.press("wol:scnew")
	e.press("wol:sctgt:g:" + g[0].ID)
	if m := e.typ("25:00"); !strings.Contains(m.Text, "HH:MM") {
		t.Errorf("bad time accepted: %q", m.Text)
	}
	m := e.typ("7:05")
	sc := e.store.Schedules()
	if len(sc) != 1 || sc[0].At != "07:05" || sc[0].Days != wol.Workdays || sc[0].Target.Kind != wol.TargetGroup {
		t.Fatalf("schedules = %+v", sc)
	}
	if !strings.Contains(m.Text, "Next run") {
		t.Errorf("schedule card = %q", m.Text)
	}
	e.press("wol:scday:" + sc[0].ID + ":5") // toggle Saturday on
	if s, _ := e.store.Schedule(sc[0].ID); !s.Days.Has(time.Saturday) {
		t.Error("Saturday not toggled")
	}
}

func TestUnauthorizedChatIsRejected(t *testing.T) {
	e := setup(t)
	before := len(e.msgr.Sent)
	e.b.Dispatch(context.Background(), bottest.Press(999, e.menu, "wol:list"))
	if len(e.msgr.Sent) != before {
		t.Fatal("bot rendered a screen for a stranger")
	}
	if a := e.msgr.Answers[len(e.msgr.Answers)-1]; !strings.Contains(a, "denied") {
		t.Errorf("answer = %q", a)
	}
}

func TestParseManual(t *testing.T) {
	d, err := parseManual("02:5e:10:aa:bb:01")
	if err != nil || d.Name != "02:5e:10:aa:bb:01" {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := parseManual("just a name"); err != wol.ErrBadMAC {
		t.Fatalf("err = %v", err)
	}
}

func TestChatStaysClean(t *testing.T) {
	e := setup(t)
	firstMenu := e.menu

	// A typed answer is deleted after it is handled.
	e.press("wol:addm")
	u := bottest.Type(chat, "PC 02:5e:10:aa:bb:01")
	e.b.Dispatch(context.Background(), u)
	e.b.Wait()
	if !e.msgr.WasDeleted(u.Message.MessageID) {
		t.Error("typed answer left in the chat")
	}
	if e.msgr.WasDeleted(firstMenu) {
		t.Error("menu deleted while editing in place")
	}

	// A command is deleted too, and its new menu replaces the old one.
	cmd := bottest.Type(chat, "/status")
	e.b.Dispatch(context.Background(), cmd)
	e.b.Wait()
	if !e.msgr.WasDeleted(cmd.Message.MessageID) {
		t.Error("command left in the chat")
	}
	if !e.msgr.WasDeleted(firstMenu) {
		t.Error("old menu not removed when a new one was sent")
	}
}

func TestLogScreen(t *testing.T) {
	e := setup(t)
	d, _ := e.store.AddDevice(wol.Device{Name: "GamePC", MAC: "02:5e:10:aa:bb:01", Notify: true})
	_ = e.store.AddEvent(wol.LogEntry{DeviceID: d.ID, Device: d.Name, Kind: wol.EventOnline, At: time.Now()})

	// Global log screen
	m := e.press("wol:log")
	if !strings.Contains(m.Text, "GamePC") {
		t.Errorf("global log screen missing event: %q", m.Text)
	}

	// Device-specific log screen
	m = e.press("wol:log:" + d.ID)
	if !strings.Contains(m.Text, "GamePC") {
		t.Errorf("device log screen missing event: %q", m.Text)
	}

	// Clear log
	e.press("wol:logclr")
	if len(e.store.Events()) != 0 {
		t.Errorf("clear log failed")
	}
}
