package wol

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Repository interfaces are split by aggregate so each consumer depends
// only on what it uses (the watcher never sees schedules, etc.).

type DeviceRepo interface {
	Devices() []Device
	Device(id string) (Device, error)
	DeviceByMAC(mac string) (Device, error)
	AddDevice(d Device) (Device, error)
	UpdateDevice(d Device) error
	DeleteDevice(id string) error
}

type GroupRepo interface {
	Groups() []Group
	Group(id string) (Group, error)
	SaveGroup(g Group) (Group, error)
	DeleteGroup(id string) error
}

type ScheduleRepo interface {
	Schedules() []Schedule
	Schedule(id string) (Schedule, error)
	SaveSchedule(s Schedule) (Schedule, error)
	DeleteSchedule(id string) error
}

type EventRepo interface {
	Events() []LogEntry
	DeviceEvents(deviceID string) []LogEntry
	AddEvent(e LogEntry) error
	ClearEvents() error
}

type data struct {
	Version   int        `json:"version"`
	Seq       int        `json:"seq"`
	Devices   []Device   `json:"devices"`
	Groups    []Group    `json:"groups"`
	Schedules []Schedule `json:"schedules"`
	Events    []LogEntry `json:"events,omitempty"`
}

// JSONStore keeps everything in one small JSON file, rewritten atomically
// (temp file + rename) on every change. Reads are served from memory.
type JSONStore struct {
	path string
	mu   sync.RWMutex
	d    data
}

var _ interface {
	DeviceRepo
	GroupRepo
	ScheduleRepo
	EventRepo
} = (*JSONStore)(nil)

// OpenJSON loads path, or starts empty if it does not exist yet.
func OpenJSON(path string) (*JSONStore, error) {
	s := &JSONStore{path: path, d: data{Version: 1}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *JSONStore) flush() error {
	raw, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// mutate applies fn under the write lock and persists; on a failed write
// the in-memory state is rolled back so memory and disk never diverge.
func (s *JSONStore) mutate(fn func(d *data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.d
	before.Devices = slices.Clone(s.d.Devices)
	before.Groups = slices.Clone(s.d.Groups)
	before.Schedules = slices.Clone(s.d.Schedules)
	before.Events = slices.Clone(s.d.Events)
	if err := fn(&s.d); err != nil {
		s.d = before
		return err
	}
	if err := s.flush(); err != nil {
		s.d = before
		return err
	}
	return nil
}

func (s *JSONStore) nextID(d *data, prefix string) string {
	d.Seq++
	return fmt.Sprintf("%s%d", prefix, d.Seq)
}

// ---- devices

func (s *JSONStore) Devices() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.d.Devices)
}

func (s *JSONStore) Device(id string) (Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Devices {
		if d.ID == id {
			return d, nil
		}
	}
	return Device{}, ErrNotFound
}

func (s *JSONStore) DeviceByMAC(mac string) (Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Devices {
		if strings.EqualFold(d.MAC, mac) {
			return d, nil
		}
	}
	return Device{}, ErrNotFound
}

func (s *JSONStore) AddDevice(d Device) (Device, error) {
	err := s.mutate(func(st *data) error {
		for _, x := range st.Devices {
			if strings.EqualFold(x.MAC, d.MAC) {
				return ErrDuplicate
			}
		}
		d.ID = s.nextID(st, "d")
		st.Devices = append(st.Devices, d)
		return nil
	})
	return d, err
}

func (s *JSONStore) UpdateDevice(d Device) error {
	return s.mutate(func(st *data) error {
		i := slices.IndexFunc(st.Devices, func(x Device) bool { return x.ID == d.ID })
		if i < 0 {
			return ErrNotFound
		}
		for _, x := range st.Devices {
			if x.ID != d.ID && strings.EqualFold(x.MAC, d.MAC) {
				return ErrDuplicate
			}
		}
		st.Devices[i] = d
		return nil
	})
}

// DeleteDevice also removes it from groups and drops its schedules.
func (s *JSONStore) DeleteDevice(id string) error {
	return s.mutate(func(st *data) error {
		n := len(st.Devices)
		st.Devices = slices.DeleteFunc(st.Devices, func(x Device) bool { return x.ID == id })
		if len(st.Devices) == n {
			return ErrNotFound
		}
		for i := range st.Groups {
			st.Groups[i].Devices = slices.DeleteFunc(st.Groups[i].Devices, func(x string) bool { return x == id })
		}
		st.Schedules = slices.DeleteFunc(st.Schedules, func(x Schedule) bool {
			return x.Target.Kind == TargetDevice && x.Target.ID == id
		})
		return nil
	})
}

// ---- groups

func (s *JSONStore) Groups() []Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Clone(s.d.Groups)
	for i := range out {
		out[i].Devices = slices.Clone(out[i].Devices)
	}
	return out
}

func (s *JSONStore) Group(id string) (Group, error) {
	for _, g := range s.Groups() {
		if g.ID == id {
			return g, nil
		}
	}
	return Group{}, ErrNotFound
}

// SaveGroup inserts (empty ID) or replaces a group.
func (s *JSONStore) SaveGroup(g Group) (Group, error) {
	err := s.mutate(func(st *data) error {
		if g.ID == "" {
			g.ID = s.nextID(st, "g")
			st.Groups = append(st.Groups, g)
			return nil
		}
		i := slices.IndexFunc(st.Groups, func(x Group) bool { return x.ID == g.ID })
		if i < 0 {
			return ErrNotFound
		}
		st.Groups[i] = g
		return nil
	})
	return g, err
}

func (s *JSONStore) DeleteGroup(id string) error {
	return s.mutate(func(st *data) error {
		n := len(st.Groups)
		st.Groups = slices.DeleteFunc(st.Groups, func(x Group) bool { return x.ID == id })
		if len(st.Groups) == n {
			return ErrNotFound
		}
		st.Schedules = slices.DeleteFunc(st.Schedules, func(x Schedule) bool {
			return x.Target.Kind == TargetGroup && x.Target.ID == id
		})
		return nil
	})
}

// ---- schedules

func (s *JSONStore) Schedules() []Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.d.Schedules)
}

func (s *JSONStore) Schedule(id string) (Schedule, error) {
	for _, x := range s.Schedules() {
		if x.ID == id {
			return x, nil
		}
	}
	return Schedule{}, ErrNotFound
}

func (s *JSONStore) SaveSchedule(sc Schedule) (Schedule, error) {
	err := s.mutate(func(st *data) error {
		if sc.ID == "" {
			sc.ID = s.nextID(st, "s")
			st.Schedules = append(st.Schedules, sc)
			return nil
		}
		i := slices.IndexFunc(st.Schedules, func(x Schedule) bool { return x.ID == sc.ID })
		if i < 0 {
			return ErrNotFound
		}
		st.Schedules[i] = sc
		return nil
	})
	return sc, err
}

func (s *JSONStore) DeleteSchedule(id string) error {
	return s.mutate(func(st *data) error {
		n := len(st.Schedules)
		st.Schedules = slices.DeleteFunc(st.Schedules, func(x Schedule) bool { return x.ID == id })
		if len(st.Schedules) == n {
			return ErrNotFound
		}
		return nil
	})
}

// ImportLegacy adds devices from the old Lua bot's [wol] section
// ("Label|MAC|iface|ip") when the store has no devices yet. It returns
// how many were imported.
func ImportLegacy(repo DeviceRepo, entries map[string]string) int {
	if len(repo.Devices()) > 0 {
		return 0
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	n := 0
	for _, k := range keys {
		parts := strings.Split(entries[k], "|")
		if len(parts) < 2 {
			continue
		}
		mac, err := NormalizeMAC(parts[1])
		if err != nil {
			continue
		}
		name, err := CleanName(parts[0])
		if err != nil {
			name = mac
		}
		d := Device{Name: name, MAC: mac, Notify: true}
		if len(parts) > 2 {
			d.Iface = strings.TrimSpace(parts[2])
		}
		if len(parts) > 3 {
			d.IP, _ = CleanIP(parts[3])
		}
		if _, err := repo.AddDevice(d); err == nil {
			n++
		}
	}
	return n
}

const maxEvents = 50

func (s *JSONStore) Events() []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.d.Events)
}

func (s *JSONStore) DeviceEvents(deviceID string) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []LogEntry
	for _, e := range s.d.Events {
		if e.DeviceID == deviceID {
			out = append(out, e)
		}
	}
	return out
}

func (s *JSONStore) AddEvent(e LogEntry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return s.mutate(func(d *data) error {
		d.Events = append(d.Events, e)
		if len(d.Events) > maxEvents {
			d.Events = d.Events[len(d.Events)-maxEvents:]
		}
		return nil
	})
}

func (s *JSONStore) ClearEvents() error {
	return s.mutate(func(d *data) error {
		d.Events = nil
		return nil
	})
}
