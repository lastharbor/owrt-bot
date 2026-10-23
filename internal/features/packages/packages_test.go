package packages

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/bottest"
	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/pkgmgr"
)

type fakePM struct {
	mu       sync.Mutex
	list     []pkgmgr.Upgrade
	upgraded []string
	release  chan struct{} // when set, Upgrade waits for it
}

func (f *fakePM) Name() string                  { return "opkg" }
func (f *fakePM) Installed(context.Context) int { return 642 }
func (f *fakePM) ListsUpdated() time.Time       { return time.Now() }
func (f *fakePM) Refresh(_ context.Context, line func(string)) error {
	line("Downloading base")
	f.mu.Lock()
	f.list = append(f.list, pkgmgr.Upgrade{Name: "luci-base", From: "1", To: "2"})
	f.mu.Unlock()
	return nil
}
func (f *fakePM) Upgradable(context.Context) ([]pkgmgr.Upgrade, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pkgmgr.Upgrade(nil), f.list...), nil
}
func (f *fakePM) Upgrade(_ context.Context, names []string, line func(string)) error {
	if f.release != nil {
		<-f.release
	}
	line("Upgrading " + strings.Join(names, " "))
	f.mu.Lock()
	f.upgraded = append(f.upgraded, names...)
	f.list = nil
	f.mu.Unlock()
	return nil
}

func setup(t *testing.T, pm *fakePM) (*bot.Bot, *bottest.Messenger, func(string)) {
	t.Helper()
	r := bot.NewRouter()
	(&Feature{PM: pm}).Register(r)
	msgr := bottest.New()
	tr := i18n.New("en").Add("en", "English", i18n.EN)
	b := bot.New(msgr, r, tr, slog.New(slog.NewTextHandler(io.Discard, nil)), bot.Options{AllowedChats: []int64{1}})
	b.Dispatch(context.Background(), bottest.Type(1, "/start"))
	menu := msgr.Last().ID
	return b, msgr, func(data string) { b.Dispatch(context.Background(), bottest.Press(1, menu, data)) }
}

func TestRefreshThenUpgradeAllSkipsKernel(t *testing.T) {
	pm := &fakePM{list: []pkgmgr.Upgrade{{Name: "kmod-nft-core", From: "a", To: "b"}}}
	b, msgr, press := setup(t, pm)

	press("pkg:show")
	if txt := msgr.Last().Text; strings.Contains(txt, "Upgrade all") {
		t.Fatalf("kernel module offered for upgrade: %q", txt)
	}
	press("pkg:refresh")
	b.Wait()
	m := msgr.Get(msgr.Sent[len(msgr.Sent)-1])
	if !strings.Contains(m.Text, "luci-base") || !strings.Contains(m.Text, "Done in") {
		t.Fatalf("after refresh: %q", m.Text)
	}
	if _, ok := bottest.Buttons(m.Keyboard)["pkg:all"]; !ok {
		t.Fatalf("no upgrade-all button: %v", bottest.Buttons(m.Keyboard))
	}
	press("pkg:all")
	press("pkg:go:all")
	b.Wait()
	if len(pm.upgraded) != 1 || pm.upgraded[0] != "luci-base" {
		t.Fatalf("upgraded %v, want only luci-base", pm.upgraded)
	}
}

func TestSecondOperationIsRefusedWhileBusy(t *testing.T) {
	pm := &fakePM{list: []pkgmgr.Upgrade{{Name: "luci-base", From: "1", To: "2"}}, release: make(chan struct{})}
	b, msgr, press := setup(t, pm)
	press("pkg:show")
	press("pkg:go:0")
	press("pkg:refresh") // while the upgrade is still running
	refused := false
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && !refused; time.Sleep(5 * time.Millisecond) {
		refused = slicesContains(msgr.AnswersSnapshot(), "already running")
	}
	if !refused {
		t.Errorf("second operation not refused, toasts %q", msgr.AnswersSnapshot())
	}
	close(pm.release)
	b.Wait()
}

func slicesContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
