//go:build linux

package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// RawWOL sends the magic packet as a raw Ethernet frame (EtherType
// 0x0842) to the broadcast MAC, exactly like `etherwake -b`.
type RawWOL struct{}

func (RawWOL) Name() string { return "ethernet" }

func (RawWOL) Send(_ context.Context, iface string, mac net.HardwareAddr) error {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return err
	}
	payload, err := MagicPacket(mac)
	if err != nil {
		return err
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(EtherTypeWOL)))
	if err != nil {
		return fmt.Errorf("raw socket: %w", err)
	}
	defer syscall.Close(fd)
	frame := ethernetFrame(broadcastMAC, ifi.HardwareAddr, EtherTypeWOL, payload)
	addr := &syscall.SockaddrLinklayer{Protocol: htons(EtherTypeWOL), Ifindex: ifi.Index, Halen: 6}
	copy(addr.Addr[:], broadcastMAC)
	return syscall.Sendto(fd, frame, 0, addr)
}

// UDPWOL sends the magic packet as a UDP broadcast bound to iface. Some
// NICs/BIOSes only react to the UDP form, so both are sent.
type UDPWOL struct{ Port int }

func (UDPWOL) Name() string { return "udp" }

func (u UDPWOL) Send(_ context.Context, iface string, mac net.HardwareAddr) error {
	payload, err := MagicPacket(mac)
	if err != nil {
		return err
	}
	port := u.Port
	if port == 0 {
		port = 9
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); err != nil {
		return err
	}
	if err := syscall.BindToDevice(fd, iface); err != nil {
		return fmt.Errorf("bind to %s: %w", iface, err)
	}
	dst := &syscall.SockaddrInet4{Port: port, Addr: [4]byte{255, 255, 255, 255}}
	return syscall.Sendto(fd, payload, 0, dst)
}

// ARPProber asks "who has ip" on iface and waits for the answer. It is
// the cheapest honest "is anything at this address" test: it needs no
// open port and is not blocked by the Windows firewall.
type ARPProber struct {
	Tries    int           // requests to send, default 3
	Interval time.Duration // between requests, default 300 ms
}

func (p ARPProber) Probe(ctx context.Context, iface string, ip net.IP) (net.HardwareAddr, error) {
	ip = ip.To4()
	if ip == nil {
		return nil, errors.New("arp: not an IPv4 address")
	}
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}
	src, err := ifaceIPv4(ifi, ip)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM, int(htons(syscall.ETH_P_ARP)))
	if err != nil {
		return nil, fmt.Errorf("arp socket: %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ARP), Ifindex: ifi.Index}); err != nil {
		return nil, err
	}
	tv := syscall.NsecToTimeval((50 * time.Millisecond).Nanoseconds())
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	dst := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ARP), Ifindex: ifi.Index, Halen: 6}
	copy(dst.Addr[:], broadcastMAC)
	req := arpRequest(ifi.HardwareAddr, src, ip)

	tries, interval := p.Tries, p.Interval
	if tries <= 0 {
		tries = 3
	}
	if interval <= 0 {
		interval = 300 * time.Millisecond
	}
	buf := make([]byte, 128)
	for range tries {
		if err := syscall.Sendto(fd, req, 0, dst); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(interval)
		for time.Now().Before(deadline) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				continue // EAGAIN on timeout
			}
			if mac, ok := parseARPReply(buf[:n], ip); ok {
				return mac, nil
			}
		}
	}
	return nil, nil // nobody answered: not an error, just absent
}
