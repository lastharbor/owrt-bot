package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/lastharbor/owrt-bot/internal/i18n"
)

func TestFooterCountdown(t *testing.T) {
	c := &Ctx{bot: &Bot{tr: i18n.New("en").Add("en", "English", i18n.EN)}}
	for left, want := range map[time.Duration]string{
		3 * time.Second:         "refresh in 3 s",
		2300 * time.Millisecond: "refresh in 3 s", // rounded up, never shows 0
		200 * time.Millisecond:  "refresh in 1 s",
	} {
		if got := c.withFooter(Screen{Text: "x"}, left).Text; !strings.Contains(got, want) {
			t.Errorf("%v left: %q, want %q", left, got, want)
		}
	}
	if got := c.withFooter(Screen{Text: "x"}, 0).Text; !strings.Contains(got, "paused") {
		t.Errorf("stopped footer: %q", got)
	}
}
