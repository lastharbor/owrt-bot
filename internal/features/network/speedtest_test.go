package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpeedTestFallsBackToWorkingServer(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // accepts the connection, never sends a byte
	}))
	defer dead.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer broken.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 32<<10)
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer good.Close()

	var progressCalls int
	st := SpeedTest{
		HTTP:     good.Client(),
		URLs:     []string{dead.URL, broken.URL, good.URL},
		Streams:  2,
		Duration: 1200 * time.Millisecond,
		Connect:  200 * time.Millisecond,
	}
	r, err := st.Run(context.Background(), func(string, int64, time.Duration) { progressCalls++ })
	if err != nil {
		t.Fatal(err)
	}
	if r.Host != strings.TrimPrefix(good.URL, "http://") || r.Bytes <= 0 || r.Mbps() <= 0 {
		t.Fatalf("result = %+v", r)
	}
	if r.Elapsed < time.Second || r.Elapsed > 2*time.Second {
		t.Errorf("window not respected: %v", r.Elapsed)
	}
	if progressCalls == 0 {
		t.Error("no progress reported")
	}
}

func TestSpeedTestReportsEveryServer(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer broken.Close()
	st := SpeedTest{HTTP: broken.Client(), URLs: []string{broken.URL, "http://127.0.0.1:1/x"}, Connect: 300 * time.Millisecond}
	_, err := st.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if msg := err.Error(); !strings.Contains(msg, "HTTP 403") || !strings.Contains(msg, "127.0.0.1:1") {
		t.Errorf("error does not name each server: %v", msg)
	}
}

// A server with small files must still be measured for the whole window.
func TestSpeedTestRepeatsSmallFiles(t *testing.T) {
	var requests atomic.Int64
	small := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write(make([]byte, 64<<10))
	}))
	defer small.Close()
	st := SpeedTest{HTTP: small.Client(), URLs: []string{small.URL}, Streams: 2, Duration: 300 * time.Millisecond}
	r, err := st.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Elapsed < 250*time.Millisecond || requests.Load() < 4 {
		t.Fatalf("stopped after the first file: %v, %d requests", r.Elapsed, requests.Load())
	}
}
