package wol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// Sender is one way of delivering a magic packet (strategy).
type Sender interface {
	Name() string
	Send(ctx context.Context, iface string, mac net.HardwareAddr) error
}

// Progress is called while waiting for a device to come up.
type Progress func(elapsed time.Duration, r Result)

// Waker sends magic packets and optionally waits for the device to
// answer, re-sending periodically because some NICs miss the first one.
type Waker struct {
	Senders  []Sender
	Presence *Presence
	LANIface string

	Burst     int           // packets per send, default 3
	Poll      time.Duration // presence check period while waiting, default 3 s
	Resend    time.Duration // re-send period while waiting, default 20 s
	sleepFunc func(ctx context.Context, d time.Duration)
}

// Send delivers the magic packet through every sender; it succeeds when
// at least one sender did.
func (w *Waker) Send(ctx context.Context, d Device) error {
	hw := d.HW()
	if hw == nil {
		return ErrBadMAC
	}
	iface := d.Iface
	if iface == "" {
		iface = w.LANIface
	}
	burst := w.Burst
	if burst <= 0 {
		burst = 3
	}
	var errs []error
	sent := false
	for i := range burst {
		if i > 0 {
			w.sleep(ctx, 100*time.Millisecond)
		}
		for _, s := range w.Senders {
			if err := s.Send(ctx, iface, hw); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
				continue
			}
			sent = true
		}
	}
	if !sent {
		return errors.Join(errs...)
	}
	return nil
}

// WakeAndWait sends, then polls until the device is online or timeout.
// It returns the last observation and how long it took.
func (w *Waker) WakeAndWait(ctx context.Context, d Device, timeout time.Duration, progress Progress) (Result, time.Duration, error) {
	start := time.Now()
	if err := w.Send(ctx, d); err != nil {
		return Result{}, 0, err
	}
	poll, resend := w.Poll, w.Resend
	if poll <= 0 {
		poll = 3 * time.Second
	}
	if resend <= 0 {
		resend = 20 * time.Second
	}
	lastSend := start
	var last Result
	for {
		w.sleep(ctx, poll)
		if ctx.Err() != nil {
			return last, time.Since(start), ctx.Err()
		}
		last = w.Presence.Check(ctx, d)
		elapsed := time.Since(start)
		if last.Status == StatusOnline || elapsed >= timeout {
			return last, elapsed, nil
		}
		if progress != nil {
			progress(elapsed, last)
		}
		if time.Since(lastSend) >= resend {
			_ = w.Send(ctx, d)
			lastSend = time.Now()
		}
	}
}

func (w *Waker) sleep(ctx context.Context, d time.Duration) {
	if w.sleepFunc != nil {
		w.sleepFunc(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
