package netx

import (
	"bytes"
	"net"
	"testing"
)

func TestMagicPacket(t *testing.T) {
	mac, _ := net.ParseMAC("02:5e:10:aa:bb:01")
	p, err := MagicPacket(mac)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 102 || !bytes.Equal(p[:6], []byte{255, 255, 255, 255, 255, 255}) {
		t.Fatalf("bad header/len %d", len(p))
	}
	for i := range 16 {
		if !bytes.Equal(p[6+i*6:12+i*6], mac) {
			t.Fatalf("repeat %d wrong", i)
		}
	}
	if _, err := MagicPacket(net.HardwareAddr{1, 2}); err == nil {
		t.Fatal("short MAC accepted")
	}
}

func TestARPRoundTrip(t *testing.T) {
	src, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	req := arpRequest(src, net.ParseIP("192.168.1.1"), net.ParseIP("192.168.1.121"))
	// Turn the request into the reply the target would send.
	reply := append([]byte(nil), req...)
	reply[7] = 2
	pcMAC, _ := net.ParseMAC("02:5e:10:aa:bb:01")
	copy(reply[8:14], pcMAC)
	copy(reply[14:18], net.ParseIP("192.168.1.121").To4())
	got, ok := parseARPReply(reply, net.ParseIP("192.168.1.121"))
	if !ok || got.String() != "02:5e:10:aa:bb:01" {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := parseARPReply(req, net.ParseIP("192.168.1.121")); ok {
		t.Fatal("request parsed as reply")
	}
}

func TestParseNeigh(t *testing.T) {
	text := `192.168.1.121 dev br-lan lladdr 02:5E:10:AA:BB:01 ref 1 used 0/0/0 probes 1 REACHABLE
192.168.1.171 dev br-lan  used 0/0/0 probes 6 FAILED
fe80::1 dev br-lan lladdr 11:22:33:44:55:66 STALE
`
	n := ParseNeigh(text)
	if len(n) != 2 {
		t.Fatalf("got %d", len(n))
	}
	if n[0].MAC != "02:5e:10:aa:bb:01" || n[0].State != NeighReachable || n[0].Dev != "br-lan" || !n[0].State.Fresh() {
		t.Errorf("row0 = %+v", n[0])
	}
	if n[1].MAC != "" || n[1].State != NeighFailed {
		t.Errorf("row1 = %+v", n[1])
	}
}
