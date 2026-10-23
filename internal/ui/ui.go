// Package ui has text widgets for Telegram messages: sparklines and bars
// that make live screens readable at a glance.
package ui

import (
	"fmt"
	"math"
	"strings"
)

var ticks = []rune("▁▂▃▄▅▆▇█")

// Spark renders values as a sparkline scaled to [lo, hi]. Pass hi <= lo
// to scale to the data's own maximum (for throughput with no fixed top).
func Spark(values []float64, lo, hi float64) string {
	if len(values) == 0 {
		return ""
	}
	if hi <= lo {
		lo, hi = 0, 0
		for _, v := range values {
			hi = math.Max(hi, v)
		}
		if hi == 0 {
			hi = 1
		}
	}
	var b strings.Builder
	for _, v := range values {
		f := (v - lo) / (hi - lo)
		i := int(math.Round(f * float64(len(ticks)-1)))
		b.WriteRune(ticks[max(0, min(len(ticks)-1, i))])
	}
	return b.String()
}

// Bar renders a percentage as a fixed-width bar: ▰▰▰▱▱▱▱▱▱▱.
func Bar(pct float64, cells int) string {
	n := int(math.Round(pct / 100 * float64(cells)))
	n = max(0, min(cells, n))
	return strings.Repeat("▰", n) + strings.Repeat("▱", cells-n)
}

// Level picks a traffic-light emoji for a percentage.
func Level(pct, warn, crit float64) string {
	switch {
	case pct >= crit:
		return "🔴"
	case pct >= warn:
		return "🟡"
	}
	return "🟢"
}

// Rate formats bytes/s using translated "unit_mbit"/"unit_kbit" templates.
func Rate(t func(string, ...any) string, bytesPerSec float64) string {
	bits := bytesPerSec * 8
	if bits >= 1e6 {
		return t("unit_mbit", "n", fmt.Sprintf("%.1f", bits/1e6))
	}
	return t("unit_kbit", "n", fmt.Sprintf("%.0f", bits/1e3))
}
