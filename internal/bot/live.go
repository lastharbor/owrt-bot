package bot

import (
	"time"

	"github.com/lastharbor/owrt-bot/internal/telegram"
)

// Render builds a screen from current data. It is called repeatedly by
// Live, from a background goroutine.
type Render func(c *Ctx) Screen

// LiveOptions tunes a live screen.
type LiveOptions struct {
	Every time.Duration // refresh period, default 3 s (Telegram tolerates ~1 edit/s per chat)
	For   time.Duration // stop after this long without a button press, default 3 min
}

func (o LiveOptions) withDefaults() LiveOptions {
	if o.Every <= 0 {
		o.Every = 3 * time.Second
	}
	if o.For <= 0 {
		o.For = 3 * time.Minute
	}
	return o
}

// Live shows render's screen and keeps it current until the user opens
// another screen or opt.For passes. Data is re-rendered every opt.Every;
// in between, once a second, only the footer changes to count down to the
// next refresh. When it stops, the footer says so and the screen's own
// refresh button (which calls Live again) resumes it.
func (c *Ctx) Live(render Render, opt LiveOptions) error {
	opt = opt.withDefaults()
	if m := c.bot.opt.LiveMax; m > 0 && opt.For > m {
		opt.For = m
	}
	// One edit per second is Telegram's per-chat comfort zone; tests use
	// millisecond periods and tick at that rate instead.
	tick := min(time.Second, opt.Every)
	cur := render(c)
	if err := c.Show(c.withFooter(cur, opt.Every)); err != nil {
		return err
	}
	c.Go(func(bg *Ctx) {
		deadline := time.Now().Add(opt.For)
		next := time.Now().Add(opt.Every)
		wait := tick
		for {
			// Never sleep past the deadline, so the "paused" frame is on time.
			select {
			case <-bg.Done():
				return
			case <-time.After(max(0, min(wait, time.Until(deadline)))):
			}
			if bg.Stale() {
				return
			}
			wait = tick
			if !time.Now().Before(deadline) {
				_ = bg.Show(bg.withFooter(render(bg), 0))
				return
			}
			if !time.Now().Before(next) {
				cur = render(bg)
				next = time.Now().Add(opt.Every)
			}
			err := bg.Show(bg.withFooter(cur, time.Until(next)))
			if ra := telegram.RetryAfter(err); ra > 0 {
				wait = ra // Telegram asked us to slow down
				next = next.Add(ra)
				continue
			}
			if err != nil {
				return
			}
		}
	})
	return nil
}

// withFooter adds the countdown, or the "paused" note when left is 0.
func (c *Ctx) withFooter(s Screen, left time.Duration) Screen {
	if left <= 0 {
		s.Text += "\n\n<i>" + c.T("live_off", "time", time.Now().Format("15:04:05")) + "</i>"
		return s
	}
	n := int((left + time.Second - 1) / time.Second) // round up: 2.3 s left reads "3 s"
	s.Text += "\n\n<i>" + c.T("live_next", "n", n) + "</i>"
	return s
}
