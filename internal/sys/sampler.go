package sys

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sample is one point of live metrics.
type Sample struct {
	At     time.Time
	CPU    float64 // percent of all cores, from /proc/stat deltas
	RxRate float64 // WAN bytes/s
	TxRate float64
}

// Sampler keeps a short history of CPU and WAN throughput so screens can
// show current values and sparklines without measuring on demand.
type Sampler struct {
	Proc  Proc
	WAN   string
	Every time.Duration // default 2 s
	Keep  int           // samples kept, default 90 (3 min at 2 s)

	mu       sync.Mutex
	hist     []Sample
	idle     uint64
	total    uint64
	rx, tx   uint64
	prevTime time.Time
}

// CPUTimes reads the aggregate "cpu" line of /proc/stat.
func (p Proc) CPUTimes() (idle, total uint64, ok bool) {
	sc := bufio.NewScanner(strings.NewReader(p.read("/proc/stat")))
	if !sc.Scan() {
		return 0, 0, false
	}
	f := strings.Fields(sc.Text())
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}
	return idle, total, true
}

func (s *Sampler) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.tick(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Sampler) tick(now time.Time) {
	idle, total, okCPU := s.Proc.CPUTimes()
	rx, tx, okNet := s.Proc.IfaceCounters(s.WAN)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.prevTime.IsZero() {
		smp := Sample{At: now}
		if okCPU && total > s.total {
			busy := float64((total - s.total) - (idle - s.idle))
			smp.CPU = 100 * busy / float64(total-s.total)
		}
		if dt := now.Sub(s.prevTime).Seconds(); okNet && dt > 0 && rx >= s.rx && tx >= s.tx {
			smp.RxRate = float64(rx-s.rx) / dt
			smp.TxRate = float64(tx-s.tx) / dt
		}
		keep := s.Keep
		if keep <= 0 {
			keep = 90
		}
		s.hist = append(s.hist, smp)
		if len(s.hist) > keep {
			s.hist = s.hist[len(s.hist)-keep:]
		}
	}
	s.idle, s.total, s.rx, s.tx, s.prevTime = idle, total, rx, tx, now
}

// Seed replaces the history (demo screenshots and tests).
func (s *Sampler) Seed(h []Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hist = append([]Sample(nil), h...)
}

// Latest returns the newest sample (zero before the second tick).
func (s *Sampler) Latest() Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hist) == 0 {
		return Sample{}
	}
	return s.hist[len(s.hist)-1]
}

// History returns up to n newest samples, oldest first.
func (s *Sampler) History(n int) []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.hist) {
		n = len(s.hist)
	}
	return append([]Sample(nil), s.hist[len(s.hist)-n:]...)
}
