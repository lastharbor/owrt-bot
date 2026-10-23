package wol

import (
	"context"
	"sync"
	"time"
)

// Store is everything the manager persists.
type Store interface {
	DeviceRepo
	GroupRepo
	ScheduleRepo
}

// Manager is the facade the UI and the scheduler use, so "wake and wait",
// "wake a group" and "power off" are implemented once.
type Manager struct {
	Store       Store
	Presence    *Presence
	Waker       *Waker
	Power       PowerController // nil when SSH key could not be set up
	Watcher     *Watcher
	WaitTimeout time.Duration // default 2 min
}

// Outcome is the final state of one wake attempt.
type Outcome struct {
	Device  Device
	Result  Result
	Elapsed time.Duration
	Err     error
}

func (o Outcome) Woke() bool { return o.Err == nil && o.Result.Status == StatusOnline }

func (m *Manager) timeout() time.Duration {
	if m.WaitTimeout > 0 {
		return m.WaitTimeout
	}
	return 2 * time.Minute
}

// Wake wakes one device and waits for it.
func (m *Manager) Wake(ctx context.Context, d Device, progress Progress) Outcome {
	if r := m.Presence.Check(ctx, d); r.Status == StatusOnline {
		return Outcome{Device: d, Result: r}
	}
	r, el, err := m.Waker.WakeAndWait(ctx, d, m.timeout(), progress)
	if err == nil && r.Status == StatusOnline {
		m.Watcher.Assume(d.ID, StatusOnline)
		m.rememberIP(d, r.IP)
	}
	return Outcome{Device: d, Result: r, Elapsed: el, Err: err}
}

// WakeAll wakes devices in parallel; progress gets a snapshot of every
// device's latest observation whenever one changes.
func (m *Manager) WakeAll(ctx context.Context, devs []Device, progress func(map[string]Result)) []Outcome {
	out := make([]Outcome, len(devs))
	var mu sync.Mutex
	latest := map[string]Result{}
	var wg sync.WaitGroup
	for i, d := range devs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = m.Wake(ctx, d, func(_ time.Duration, r Result) {
				if progress == nil {
					return
				}
				mu.Lock()
				latest[d.ID] = r
				snap := make(map[string]Result, len(latest))
				for k, v := range latest {
					snap[k] = v
				}
				mu.Unlock()
				progress(snap)
			})
		}()
	}
	wg.Wait()
	return out
}

// Resolve returns a display name and the devices behind a target.
func (m *Manager) Resolve(t Target) (string, []Device, error) {
	switch t.Kind {
	case TargetDevice:
		d, err := m.Store.Device(t.ID)
		return d.Name, []Device{d}, err
	case TargetGroup:
		g, err := m.Store.Group(t.ID)
		if err != nil {
			return "", nil, err
		}
		var devs []Device
		for _, id := range g.Devices {
			if d, err := m.Store.Device(id); err == nil {
				devs = append(devs, d)
			}
		}
		return g.Name, devs, nil
	}
	return "", nil, ErrNotFound
}

// PowerAction sends a power command to the device's current IP.
func (m *Manager) PowerAction(ctx context.Context, d Device, a Action) error {
	if m.Power == nil || d.Power == nil {
		return ErrNoPower
	}
	r := m.Presence.Check(ctx, d)
	if r.IP == "" {
		return ErrBadIP
	}
	if err := m.Power.Do(ctx, d, r.IP, a); err != nil {
		return err
	}
	if a != ActReboot {
		m.Watcher.Assume(d.ID, StatusOffline)
	}
	return nil
}

// PowerChecker is implemented by controllers that can test a login.
type PowerChecker interface {
	Check(ctx context.Context, d Device, ip string) error
}

// CheckPower logs in to d at ip without changing anything.
func (m *Manager) CheckPower(ctx context.Context, d Device, ip string) error {
	pc, ok := m.Power.(PowerChecker)
	if !ok || m.Power == nil {
		return ErrNoPower
	}
	if ip == "" {
		ip = m.Presence.Check(ctx, d).IP
	}
	return pc.Check(ctx, d, ip)
}

// rememberIP stores the address the device answered on, so the next probe
// does not depend on the neighbour table.
func (m *Manager) rememberIP(d Device, ip string) {
	if ip == "" || ip == d.IP {
		return
	}
	if cur, err := m.Store.Device(d.ID); err == nil {
		cur.IP = ip
		_ = m.Store.UpdateDevice(cur)
	}
}
