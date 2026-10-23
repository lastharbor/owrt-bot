// Package netx holds LAN primitives: Wake-on-LAN magic packets, ARP
// probes and the kernel neighbour table. Packet building and parsing are
// pure functions (tested anywhere); socket code lives in *_linux.go.
package netx

import (
	"encoding/binary"
	"errors"
	"net"
)

// EtherTypeWOL is the EtherType etherwake uses for raw magic packets.
const EtherTypeWOL = 0x0842

var broadcastMAC = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// MagicPacket builds the 102-byte payload: 6×0xFF then the MAC 16 times.
func MagicPacket(mac net.HardwareAddr) ([]byte, error) {
	if len(mac) != 6 {
		return nil, errors.New("netx: MAC must be 6 bytes")
	}
	p := make([]byte, 6, 6+16*6)
	for i := range 6 {
		p[i] = 0xff
	}
	for range 16 {
		p = append(p, mac...)
	}
	return p, nil
}

// ethernetFrame wraps payload into an Ethernet II frame.
func ethernetFrame(dst, src net.HardwareAddr, etherType uint16, payload []byte) []byte {
	f := make([]byte, 14, 14+len(payload))
	copy(f[0:6], dst)
	copy(f[6:12], src)
	binary.BigEndian.PutUint16(f[12:14], etherType)
	return append(f, payload...)
}

// arpRequest builds an ARP who-has payload (no Ethernet header).
func arpRequest(srcMAC net.HardwareAddr, srcIP, dstIP net.IP) []byte {
	p := make([]byte, 28)
	binary.BigEndian.PutUint16(p[0:2], 1)      // Ethernet
	binary.BigEndian.PutUint16(p[2:4], 0x0800) // IPv4
	p[4], p[5] = 6, 4
	binary.BigEndian.PutUint16(p[6:8], 1) // request
	copy(p[8:14], srcMAC)
	copy(p[14:18], srcIP.To4())
	// target MAC stays zero
	copy(p[24:28], dstIP.To4())
	return p
}

// parseARPReply returns the sender MAC if p is a reply from ip.
func parseARPReply(p []byte, ip net.IP) (net.HardwareAddr, bool) {
	if len(p) < 28 || binary.BigEndian.Uint16(p[6:8]) != 2 {
		return nil, false
	}
	if !net.IP(p[14:18]).Equal(ip.To4()) {
		return nil, false
	}
	return net.HardwareAddr(append([]byte(nil), p[8:14]...)), true
}

// ifaceIPv4 picks the interface address in the same subnet as target, or
// its first IPv4 address.
func ifaceIPv4(ifi *net.Interface, target net.IP) (net.IP, error) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, err
	}
	var first net.IP
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil {
			continue
		}
		if first == nil {
			first = ipn.IP.To4()
		}
		if target != nil && ipn.Contains(target) {
			return ipn.IP.To4(), nil
		}
	}
	if first == nil {
		return nil, errors.New("netx: no IPv4 address on " + ifi.Name)
	}
	return first, nil
}

// InterfaceIPv4 returns the first IPv4 address of the named interface.
func InterfaceIPv4(name string) (net.IP, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	return ifaceIPv4(ifi, nil)
}
