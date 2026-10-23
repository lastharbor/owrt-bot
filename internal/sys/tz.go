package sys

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var posixTZ = regexp.MustCompile(`^([A-Za-z]{3,}|<[^>]+>)([+-]?\d{1,2})(?::(\d{2}))?`)

// ParsePOSIXTZ handles the DST-less form OpenWrt writes to /etc/TZ, e.g.
// "MSK-3" (POSIX signs are inverted: MSK-3 means UTC+3). Rules with DST
// are reduced to their standard offset.
func ParsePOSIXTZ(s string) (*time.Location, bool) {
	m := posixTZ.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil, false
	}
	h, _ := strconv.Atoi(m[2])
	min := 0
	if m[3] != "" {
		min, _ = strconv.Atoi(m[3])
	}
	off := h*3600 + min*60*sign(h, m[2])
	return time.FixedZone(strings.Trim(m[1], "<>"), -off), true
}

func sign(h int, raw string) int {
	if h < 0 || strings.HasPrefix(raw, "-") {
		return -1
	}
	return 1
}

// LocalZone returns the router's zone: Go has no zoneinfo on OpenWrt and
// procd does not export TZ, so without this time.Local is UTC.
func LocalZone(path string) *time.Location {
	if b, err := os.ReadFile(path); err == nil {
		if loc, ok := ParsePOSIXTZ(string(b)); ok {
			return loc
		}
	}
	return time.Local
}
