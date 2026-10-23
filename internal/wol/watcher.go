package wol

import (
	"context"
	"sync"
	"time"
)

// Event is a confirmed on/off transition of a device.
type Event struct {
	Device Device
	From   Status
	To     Status
	At     time.Time
}

// Listener receives events (observer pattern).
type Listener func(Event)

// Watcher polls all devices, keeps the presence cache warm and notifies
// listeners when a device changes state. A change must be seen twice in a
// row before it is reported, so a single lost ping does not spam the chat.
type Watcher struct {
	Repo     DeviceRepo
	Presence *Presence
	Interval time.Duration // default 30 s

	mu        sync.Mutex
	listeners []Listener
	stable    map[string]Status
	candidate map[string]Status
}

func (w *Watcher) Subscribe(l Listener) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.listeners = append(w.listeners, l)
}

// Run polls until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	iv := w.Interval
	if iv <= 0 {
		iv = 30 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		w.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one observation round (exported for tests).
func (w *Watcher) Tick(ctx context.Context) {
	devs := w.Repo.Devices()
	results := w.Presence.CheckAll(ctx, devs)
	if ctx.Err() != nil {
		return
	}
	var events []Event
	w.mu.Lock()
	if w.stable == nil {
		w.stable, w.candidate = map[string]Status{}, map[string]Status{}
	}
	for _, d := range devs {
		r := results[d.ID]
		if ev, ok := w.observe(d, r); ok && d.Notify {
			events = append(events, ev)
		}
	}
	listeners := append([]Listener(nil), w.listeners...)
	w.mu.Unlock()
	for _, ev := range events {
		for _, l := range listeners {
			l(ev)
		}
	}
}

// observe applies the two-in-a-row rule. Must hold w.mu.
func (w *Watcher) observe(d Device, r Result) (Event, bool) {
	if r.Status == StatusUnknown {
		return Event{}, false
	}
	prev, known := w.stable[d.ID]
	if !known {
		w.stable[d.ID] = r.Status // first sighting is the baseline, not news
		return Event{}, false
	}
	if up(prev) == up(r.Status) {
		w.stable[d.ID] = r.Status // e.g. offline -> asleep is not worth a message
		delete(w.candidate, d.ID)
		return Event{}, false
	}
	if c, ok := w.candidate[d.ID]; !ok || up(c) != up(r.Status) {
		w.candidate[d.ID] = r.Status
		return Event{}, false
	}
	delete(w.candidate, d.ID)
	w.stable[d.ID] = r.Status
	return Event{Device: d, From: prev, To: r.Status, At: r.At}, true
}

// Assume records a state change the user caused (wake, shutdown) as the
// new baseline, so the chat is not told about it a second time.
func (w *Watcher) Assume(id string, s Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stable == nil {
		w.stable, w.candidate = map[string]Status{}, map[string]Status{}
	}
	w.stable[id] = s
	delete(w.candidate, id)
}

// Forget drops tracking state of a deleted device.
func (w *Watcher) Forget(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.stable, id)
	delete(w.candidate, id)
}

func up(s Status) bool { return s == StatusOnline }
