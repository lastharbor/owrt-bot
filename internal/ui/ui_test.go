package ui

import (
	"testing"
	"unicode/utf8"
)

func TestSpark(t *testing.T) {
	if got := Spark([]float64{0, 50, 100}, 0, 100); got != "▁▅█" {
		t.Errorf("fixed scale: %q", got)
	}
	if got := Spark([]float64{1, 2, 4}, 0, 0); got != "▃▅█" {
		t.Errorf("auto scale: %q", got)
	}
	if got := Spark([]float64{0, 0}, 0, 0); got != "▁▁" {
		t.Errorf("all zero: %q", got)
	}
	if Spark(nil, 0, 1) != "" {
		t.Error("empty input")
	}
}

func TestBar(t *testing.T) {
	if got := Bar(42, 10); got != "▰▰▰▰▱▱▱▱▱▱" || utf8.RuneCountInString(got) != 10 {
		t.Errorf("bar: %q", got)
	}
	if Bar(150, 4) != "▰▰▰▰" || Bar(-5, 4) != "▱▱▱▱" {
		t.Error("clamping")
	}
}
