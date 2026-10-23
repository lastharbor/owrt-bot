package pkgmgr

import "testing"

func TestParseOpkg(t *testing.T) {
	text := `luci-base - 26.278.1~abc - 26.286.75782~0e65379
kmod-nft-core - 6.6.110-r1 - 6.6.112-r1
Collected errors:
 * pkg_hash_check_unresolved: cannot find dependency kmod-sound-core for alsa-lib
`
	got := ParseOpkg(text)
	if len(got) != 2 || got[0].Name != "luci-base" || got[0].To != "26.286.75782~0e65379" || got[0].Kernel() || !got[1].Kernel() {
		t.Fatalf("got %+v", got)
	}
}

func TestParseApk(t *testing.T) {
	text := `luci-base-26.1.2-r2 aarch64 {luci} (Apache-2.0) [upgradable from: luci-base-25.9.0-r1]
kmod-nft-core-6.6.112-r1 aarch64 {linux} (GPL-2.0) [upgradable from: kmod-nft-core-6.6.110-r1]
busybox-1.37.0-r3 aarch64 {busybox} (GPL-2.0) [installed]
`
	got := ParseApk(text)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0] != (Upgrade{Name: "luci-base", From: "25.9.0-r1", To: "26.1.2-r2"}) || got[1].Name != "kmod-nft-core" || !got[1].Kernel() {
		t.Fatalf("got %+v", got)
	}
}
