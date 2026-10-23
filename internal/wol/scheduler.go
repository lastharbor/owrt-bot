package wol

import (
	"context"
	"log/slog"
	"time"
)

// catchUp is how late a schedule may still fire (router just booted, or
// the bot restarted at 07:31 for a 07:30 wake).
const catchUp = 10 * time.Minute

// Due reports whether s should fire at now.
func Due(s Schedule, now time.Time) bool {
	if !s.Enabled || !s.Days.Has(now.Weekday()) || s.LastRun == now.Format(time.DateOnly) {
		return false
	}
	at, err := time.ParseInLocation("15:04", s.At, now.Location())
	if err != nil {
		return false
	}
	fire := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
	return !now.Before(fire) && now.Sub(fire) < catchUp
}

// Next returns the next firing time after now (zero if never).
func Next(s Schedule, now time.Time) time.Time {
	at, err := time.ParseInLocation("15:04", s.At, now.Location())
	if err != nil || !s.Enabled || s.Days == 0 {
		return time.Time{}
	}
	for i := range 8 {
		day := now.AddDate(0, 0, i)
		fire := time.Date(day.Year(), day.Month(), day.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
		if s.Days.Has(fire.Weekday()) && fire.After(now) {
			return fire
		}
	}
	return time.Time{}
}

// Scheduler fires due schedules. What "firing" means is injected, so the
// scheduler does not depend on the waker or on Telegram.
type Scheduler struct {
	Repo ScheduleRepo
	Fire func(ctx context.Context, s Schedule)
	Now  func() time.Time
	Log  *slog.Logger
}

func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Scheduler) Tick(ctx context.Context) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	for _, sc := range s.Repo.Schedules() {
		if !Due(sc, now) {
			continue
		}
		// Mark first: a crash mid-wake must not re-fire it in a loop.
		sc.LastRun = now.Format(time.DateOnly)
		if _, err := s.Repo.SaveSchedule(sc); err != nil {
			if s.Log != nil {
				s.Log.Error("schedule save failed", "id", sc.ID, "err", err)
			}
			continue
		}
		s.Fire(ctx, sc)
	}
}
