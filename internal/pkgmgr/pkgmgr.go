// Package pkgmgr wraps the router's package manager: opkg on OpenWrt up
// to 24.10, apk from 25.x. Detect picks the one that is installed, so the
// UI never cares which it is talking to.
package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/sys"
)

// Upgrade is one package with a newer version available.
type Upgrade struct {
	Name, From, To string
}

// Kernel reports packages that must match the running kernel exactly and
// are therefore upgraded with the firmware, not one by one.
func (u Upgrade) Kernel() bool { return strings.HasPrefix(u.Name, "kmod-") }

// Manager is one package manager (strategy).
type Manager interface {
	Name() string
	Installed(ctx context.Context) int
	// ListsUpdated is when package lists were last downloaded (zero if unknown).
	ListsUpdated() time.Time
	Refresh(ctx context.Context, line func(string)) error
	Upgradable(ctx context.Context) ([]Upgrade, error)
	Upgrade(ctx context.Context, names []string, line func(string)) error
}

// Detect returns apk when it exists, opkg otherwise.
func Detect(r sys.Runner, s sys.StreamRunner) Manager {
	for _, p := range []string{"/usr/bin/apk", "/sbin/apk", "/bin/apk"} {
		if _, err := os.Stat(p); err == nil {
			return &Apk{R: r, S: s}
		}
	}
	return &Opkg{R: r, S: s, Conf: "/etc/opkg.conf", Status: "/usr/lib/opkg/status"}
}

// ---- opkg

type Opkg struct {
	R      sys.Runner
	S      sys.StreamRunner
	Conf   string
	Status string
}

func (o *Opkg) Name() string { return "opkg" }

func (o *Opkg) Installed(context.Context) int {
	b, _ := os.ReadFile(o.Status)
	return strings.Count("\n"+string(b), "\nPackage: ")
}

// listsDir reads "lists_dir ext /path" from opkg.conf.
func (o *Opkg) listsDir() string {
	b, _ := os.ReadFile(o.Conf)
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 && f[0] == "lists_dir" {
			return f[2]
		}
	}
	return "/var/opkg-lists"
}

func (o *Opkg) ListsUpdated() time.Time { return newest(o.listsDir()) }

func (o *Opkg) Refresh(ctx context.Context, line func(string)) error {
	return o.S.Stream(ctx, line, "opkg", "update")
}

func (o *Opkg) Upgradable(ctx context.Context) ([]Upgrade, error) {
	out, err := o.R.Run(ctx, "opkg", "list-upgradable")
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return ParseOpkg(string(out)), nil
}

func (o *Opkg) Upgrade(ctx context.Context, names []string, line func(string)) error {
	return o.S.Stream(ctx, line, "opkg", append([]string{"upgrade"}, names...)...)
}

// ParseOpkg reads `opkg list-upgradable`: "name - old - new". opkg's
// "Collected errors" about unrelated feed packages are skipped.
func ParseOpkg(text string) []Upgrade {
	var out []Upgrade
	for _, l := range strings.Split(text, "\n") {
		p := strings.Split(strings.TrimSpace(l), " - ")
		if len(p) == 3 && !strings.ContainsAny(p[0], " *") {
			out = append(out, Upgrade{Name: p[0], From: p[1], To: p[2]})
		}
	}
	return out
}

// ---- apk

type Apk struct {
	R sys.Runner
	S sys.StreamRunner
}

func (a *Apk) Name() string { return "apk" }

func (a *Apk) Installed(ctx context.Context) int {
	out, _ := a.R.Run(ctx, "apk", "info")
	return len(sys.Lines(out))
}

func (a *Apk) ListsUpdated() time.Time { return newest("/var/cache/apk") }

func (a *Apk) Refresh(ctx context.Context, line func(string)) error {
	return a.S.Stream(ctx, line, "apk", "update")
}

func (a *Apk) Upgradable(ctx context.Context) ([]Upgrade, error) {
	out, err := a.R.Run(ctx, "apk", "list", "--upgradable")
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return ParseApk(string(out)), nil
}

func (a *Apk) Upgrade(ctx context.Context, names []string, line func(string)) error {
	return a.S.Stream(ctx, line, "apk", append([]string{"add", "--upgrade"}, names...)...)
}

// ParseApk reads `apk list --upgradable`:
// "luci-base-26.1-r2 aarch64 {luci} (Apache-2.0) [upgradable from: luci-base-25.9-r1]".
func ParseApk(text string) []Upgrade {
	var out []Upgrade
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(l)
		_, from, ok := strings.Cut(l, "[upgradable from: ")
		if len(f) == 0 || !ok {
			continue
		}
		name, to := splitApk(f[0])
		_, was := splitApk(strings.TrimSuffix(strings.TrimSpace(from), "]"))
		out = append(out, Upgrade{Name: name, From: was, To: to})
	}
	return out
}

// splitApk splits "luci-base-26.1-r2" into "luci-base" and "26.1-r2": apk
// versions contain no '-' except the "-rN" release suffix.
func splitApk(s string) (name, version string) {
	base, rel := s, ""
	if i := strings.LastIndex(s, "-r"); i > 0 {
		base, rel = s[:i], s[i:]
	}
	if i := strings.LastIndex(base, "-"); i > 0 {
		return base[:i], base[i+1:] + rel
	}
	return s, ""
}

func newest(dir string) time.Time {
	var t time.Time
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && info.ModTime().After(t) {
			t = info.ModTime()
		}
	}
	return t
}
