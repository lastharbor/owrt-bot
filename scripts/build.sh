#!/bin/sh
# Builds release binaries for common OpenWrt targets into dist/ plus
# checksums.txt, the files install.sh downloads.
#   scripts/build.sh v0.2.0
set -eu

VERSION=${1:-dev}
OUT=dist
rm -rf "$OUT" && mkdir -p "$OUT"

build() { # name GOARCH [extra env...]
	name=$1 arch=$2
	shift 2
	echo "building $name"
	env CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "$@" \
		go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/owrt-bot-linux-$name" ./cmd/owrt-bot
}

build arm64 arm64
build amd64 amd64
build armv7 arm GOARM=7
build mipsle mipsle GOMIPS=softfloat
build mips mips GOMIPS=softfloat

# LuCI page. Files only, no directory entries: the archive is unpacked
# into / and must not touch the modes of /www or /usr.
(cd luci && find . -type f | sed 's|^\./||' | sort >../"$OUT"/.luci-files)
tar -czf "$OUT/owrt-bot-luci.tar.gz" --owner=0 --group=0 --mode=644 -C luci -T "$OUT/.luci-files"
rm -f "$OUT/.luci-files"

# sha256sum on Windows marks binary mode as "*name"; keep the plain form.
(cd "$OUT" && sha256sum owrt-bot-* | sed 's/ [*]/  /' >checksums.txt)
cat "$OUT/checksums.txt"
