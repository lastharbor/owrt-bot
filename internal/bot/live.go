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

// Live shows render's screen and keeps re-rendering it in place until the
// user opens another screen or opt.For passes. A footer tells the user
// the screen is live; when it stops, the footer says so and the screen's
// own refresh button (which calls Live again) resumes it.
func (c *Ctx) Live(render Render, opt LiveOptions) error {
	opt = opt.withDefaults()
	if m := c.bot.opt.LiveMax; m > 0 && opt.For > m {
		opt.For = m
	}
	first := render(c)
	if err := c.Show(c.withFooter(first, true, opt)); err != nil {
		return err
	}
	last := first.Text
	c.Go(func(bg *Ctx) {
		deadline := time.Now().Add(opt.For)
		wait := opt.Every
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
			wait = opt.Every
			s := render(bg)
			done := !time.Now().Before(deadline)
			if s.Text == last && !done {
				continue // nothing changed: skip the edit, save the rate limit
			}
			err := bg.Show(bg.withFooter(s, !done, opt))
			if ra := telegram.RetryAfter(err); ra > 0 {
				wait = ra // Telegram asked us to slow down
				continue
			}
			if err != nil || done {
				return
			}
			last = s.Text
		}
	})
	return nil
}

func (c *Ctx) withFooter(s Screen, live bool, opt LiveOptions) Screen {
	now := time.Now().Format("15:04:05")
	if live {
		s.Text += "\n\n<i>" + c.T("live_on", "n", int(opt.Every.Seconds()), "time", now) + "</i>"
	} else {
		s.Text += "\n\n<i>" + c.T("live_off", "time", now) + "</i>"
	}
	return s
}
