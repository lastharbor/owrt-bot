//go:build !linux

package netx

import (
	"context"
	"errors"
	"net"
	"time"
)

var errUnsupported = errors.New("netx: raw sockets need Linux")

type RawWOL struct{}

func (RawWOL) Name() string                                         { return "ethernet" }
func (RawWOL) Send(context.Context, string, net.HardwareAddr) error { return errUnsupported }

type UDPWOL struct{ Port int }

func (UDPWOL) Name() string                                         { return "udp" }
func (UDPWOL) Send(context.Context, string, net.HardwareAddr) error { return errUnsupported }

type ARPProber struct {
	Tries    int
	Interval time.Duration
}

func (ARPProber) Probe(context.Context, string, net.IP) (net.HardwareAddr, error) {
	return nil, errUnsupported
}
