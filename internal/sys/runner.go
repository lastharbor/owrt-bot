// Package sys adapts the OpenWrt userland (procfs, uci, opkg, ...) to
// small Go interfaces so features never build shell strings.
package sys

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
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

// StreamRunner runs long commands (package updates) and reports output
// line by line while they run.
type StreamRunner interface {
	Stream(ctx context.Context, line func(string), name string, args ...string) error
}

// Stream runs name with stdout and stderr merged, calling line for every
// output line. There is no built-in timeout: ctx bounds it.
func (e Exec) Stream(ctx context.Context, line func(string), name string, args ...string) error {
	pr, pw := io.Pipe()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				line(l)
			}
		}
		_, _ = io.Copy(io.Discard, pr) // never block the child on a full pipe
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
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
