// Package logx provides a size-rotated log file for slog, so a daemon on
// a router never fills /var/log.
package logx

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// RotatingFile appends to path and moves it to path.1 past maxBytes.
type RotatingFile struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
	f        *os.File
	size     int64
}

func OpenRotating(path string, maxBytes int64) (*RotatingFile, error) {
	r := &RotatingFile{path: path, maxBytes: maxBytes}
	return r, r.open()
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxBytes {
		r.f.Close()
		_ = os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Tail returns the last n lines of the current log.
func (r *RotatingFile) Tail(n int) string {
	f, err := os.Open(r.path)
	if err != nil {
		return ""
	}
	defer f.Close()
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	return strings.Join(ring, "\n")
}

// New builds a text logger writing to w at the given level name.
func New(w io.Writer, level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv}))
}
