package netx

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"
)

// ExecPinger runs BusyBox `ping -c 1 -W 1`; exit code 0 means a reply.
type ExecPinger struct{ R runner }

func (p ExecPinger) Ping(ctx context.Context, ip string) bool {
	_, err := p.R.Run(ctx, "ping", "-c", "1", "-W", "1", ip)
	return err == nil
}

// TCPAlive reports whether the host's TCP stack answers on port: an
// accepted connection or an explicit refusal (RST) both prove the OS is
// awake, a timeout proves nothing.
func TCPAlive(ctx context.Context, ip string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err == nil {
		c.Close()
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}
