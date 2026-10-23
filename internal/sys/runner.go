// Package sys adapts the OpenWrt userland (procfs, uci, opkg, ...) to
// small Go interfaces so features never build shell strings.
package sys

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a program with discrete arguments, never through a
// shell, so names like "@wifi-iface[0]" or user-typed text cannot be
// interpreted as shell syntax.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Exec is the real Runner.
type Exec struct {
	Timeout time.Duration // per command; 0 = 30 s
}

func (e Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	t := e.Timeout
	if t == 0 {
		t = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// Lines splits command output into trimmed non-empty lines.
func Lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
