// Package wol is the Wake-on-LAN domain: devices, groups, schedules and
// the services that wake, watch and power them. It knows nothing about
// Telegram; the UI lives in internal/features/wolui.
package wol

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

var (
	ErrNotFound  = errors.New("wol: not found")
	ErrDuplicate = errors.New("wol: device with this MAC already exists")
	ErrBadMAC    = errors.New("wol: invalid MAC address")
	ErrBadName   = errors.New("wol: name must be 1-32 characters")
	ErrBadIP     = errors.New("wol: invalid IPv4 address")
	ErrBadTime   = errors.New("wol: time must be HH:MM")
)

// OS selects the shutdown/reboot commands sent over SSH.
type OS string

const (
	Windows OS = "windows"
	Linux   OS = "linux"
)

// Power describes how to reach a device to turn it off. Nil means the
// device can only be woken.
type Power struct {
	User string `json:"user"`
	Port int    `json:"port,omitempty"` // default 22
	OS   OS     `json:"os"`
}

func (p *Power) PortOrDefault() int {
	if p == nil || p.Port == 0 {
		return 22
	}
	return p.Port
}

// Device is a machine that can be woken.
type Device struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MAC    string `json:"mac"`             // normalised aa:bb:cc:dd:ee:ff
	IP     string `json:"ip,omitempty"`    // last known; refreshed from ARP
	Iface  string `json:"iface,omitempty"` // empty = LAN default
	Notify bool   `json:"notify"`          // tell the chat when it goes on/off
	Power  *Power `json:"power,omitempty"`
}

// HW returns the parsed MAC.
func (d Device) HW() net.HardwareAddr {
	hw, _ := net.ParseMAC(d.MAC)
	return hw
}

// Group wakes several devices with one button.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Devices []string `json:"devices"`
}

// Weekdays is a bit set, Monday = bit 0 ... Sunday = bit 6.
type Weekdays uint8

const (
	Workdays Weekdays = 0b0011111
	AllWeek  Weekdays = 0b1111111
)

// bit maps time.Weekday (Sunday=0) onto our Monday-first bits.
func bit(d time.Weekday) Weekdays { return 1 << ((int(d) + 6) % 7) }

func (w Weekdays) Has(d time.Weekday) bool        { return w&bit(d) != 0 }
func (w Weekdays) Toggle(d time.Weekday) Weekdays { return w ^ bit(d) }

// Schedule wakes a target at a local time on chosen weekdays.
type Schedule struct {
	ID      string   `json:"id"`
	Target  Target   `json:"target"`
	At      string   `json:"at"` // "07:30", router local time
	Days    Weekdays `json:"days"`
	Enabled bool     `json:"enabled"`
	LastRun string   `json:"last_run,omitempty"` // "2006-01-02" of the last firing
}

// TargetKind distinguishes device and group targets.
type TargetKind string

const (
	TargetDevice TargetKind = "device"
	TargetGroup  TargetKind = "group"
)

// Target points at a device or a group by id.
type Target struct {
	Kind TargetKind `json:"kind"`
	ID   string     `json:"id"`
}

// NormalizeMAC accepts aa:bb.., AA-BB-.., aabb.cc.. and aabbccddeeff and
// returns lowercase colon form.
func NormalizeMAC(s string) (string, error) {
	s = strings.TrimSpace(s)
	clean := strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(s)
	if len(clean) != 12 {
		return "", ErrBadMAC
	}
	hw, err := net.ParseMAC(strings.Join([]string{
		clean[0:2], clean[2:4], clean[4:6], clean[6:8], clean[8:10], clean[10:12],
	}, ":"))
	if err != nil {
		return "", ErrBadMAC
	}
	if hw[0]&1 == 1 { // multicast/broadcast address cannot be a NIC
		return "", ErrBadMAC
	}
	return hw.String(), nil
}

// CleanName trims a user-supplied name and checks its length.
func CleanName(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || len([]rune(s)) > 32 {
		return "", ErrBadName
	}
	return s, nil
}

// CleanIP accepts an empty string (unknown) or a dotted IPv4.
func CleanIP(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return "", ErrBadIP
	}
	return ip.To4().String(), nil
}

// ParseClock validates "7:30" / "07:30" and returns "07:30".
func ParseClock(s string) (string, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		if t, err = time.Parse("3:04", strings.TrimSpace(s)); err != nil {
			return "", ErrBadTime
		}
	}
	return fmt.Sprintf("%02d:%02d", t.Hour(), t.Minute()), nil
}
