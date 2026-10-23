package bot_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/bottest"
	"github.com/lastharbor/owrt-bot/internal/i18n"
)

func TestLiveScreen(t *testing.T) {
	var value atomic.Int64
	var renders atomic.Int64
	r := bot.NewRouter()
	r.Callback("t:live", func(c *bot.Ctx) error {
		return c.Live(func(c *bot.Ctx) bot.Screen {
			renders.Add(1)
			return bot.Screen{Text: "value=" + string(rune('0'+value.Load()))}
		}, bot.LiveOptions{Every: 10 * time.Millisecond, For: 200 * time.Millisecond})
	})
	r.Callback("t:other", func(c *bot.Ctx) error { return c.Show(bot.Screen{Text: "other"}) })

	msgr := bottest.New()
	tr := i18n.New("en").Add("en", "English", i18n.EN)
	b := bot.New(msgr, r, tr, slog.New(slog.NewTextHandler(io.Discard, nil)), bot.Options{AllowedChats: []int64{1}})
	ctx := context.Background()
	b.Dispatch(ctx, bottest.Type(1, "/start"))
	menu := msgr.Last().ID

	b.Dispatch(ctx, bottest.Press(1, menu, "t:live"))
	if got := msgr.Get(menu).Text; !strings.HasPrefix(got, "value=0") || !strings.Contains(got, "LIVE") {
		t.Fatalf("first frame = %q", got)
	}
	value.Store(5)
	deadline := time.Now().Add(time.Second)
	for !strings.HasPrefix(msgr.Get(menu).Text, "value=5") {
		if time.Now().After(deadline) {
			t.Fatalf("screen never updated: %q", msgr.Get(menu).Text)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Leaving the screen stops the loop: "other" must not be overwritten.
	b.Dispatch(ctx, bottest.Press(1, menu, "t:other"))
	before := renders.Load()
	time.Sleep(50 * time.Millisecond)
	b.Wait()
	if got := msgr.Get(menu).Text; got != "other" {
		t.Fatalf("live loop overwrote another screen: %q", got)
	}
	if renders.Load() > before+1 {
		t.Errorf("loop kept rendering after the user left: %d extra", renders.Load()-before)
	}
}

func TestLivePausesAfterTimeout(t *testing.T) {
	r := bot.NewRouter()
	r.Callback("t:live", func(c *bot.Ctx) error {
		return c.Live(func(*bot.Ctx) bot.Screen { return bot.Screen{Text: "same"} },
			bot.LiveOptions{Every: 10 * time.Millisecond, For: 40 * time.Millisecond})
	})
	msgr := bottest.New()
	tr := i18n.New("en").Add("en", "English", i18n.EN)
	b := bot.New(msgr, r, tr, slog.New(slog.NewTextHandler(io.Discard, nil)), bot.Options{AllowedChats: []int64{1}})
	b.Dispatch(context.Background(), bottest.Type(1, "/start"))
	menu := msgr.Last().ID
	b.Dispatch(context.Background(), bottest.Press(1, menu, "t:live"))
	b.Wait()
	if got := msgr.Get(menu).Text; !strings.Contains(got, "paused") {
		t.Fatalf("no paused footer after timeout: %q", got)
	}
}
