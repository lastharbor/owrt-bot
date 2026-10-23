package bot

import (
	"fmt"
	"log/slog"
	"time"
)

// Recover turns a handler panic into an error so one bad button cannot
// take the whole bot down.
func Recover() Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			return next(c)
		}
	}
}

// Timing logs every action with its duration, which is how slow screens
// show up in the log instead of in complaints.
func Timing(log *slog.Logger) Middleware {
	return func(next Handler) Handler {
		return func(c *Ctx) error {
			start := time.Now()
			err := next(c)
			log.Info("action", "chat", c.Chat, "route", c.Route, "args", c.Args,
				"ms", time.Since(start).Milliseconds(), "err", err)
			return err
		}
	}
}
