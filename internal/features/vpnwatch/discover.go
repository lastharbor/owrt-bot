package vpnwatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrNoController means no running engine exposes a Clash API: nothing to
// watch and nothing to report.
var ErrNoController = errors.New("vpnwatch: no Clash API found")

// Discover finds a Clash API through running processes instead of known
// paths, so it works for whatever wrapper started the engine (ZeroBlock,
// Podkop, OpenClash, Nikki or a hand-made service): the command line names
// the config, and the config names the controller and its secret.
func Discover(procRoot string) EndpointFunc {
	return func() (Endpoint, error) {
		ents, err := os.ReadDir(procRoot)
		if err != nil {
			return Endpoint{}, err
		}
		var pids []int
		for _, e := range ents {
			if pid, err := strconv.Atoi(e.Name()); err == nil {
				pids = append(pids, pid)
			}
		}
		sort.Ints(pids) // the oldest engine wins when there are several
		for _, pid := range pids {
			dir := filepath.Join(procRoot, strconv.Itoa(pid))
			raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
			if err != nil || len(raw) == 0 {
				continue
			}
			args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
			cwd, _ := os.Readlink(filepath.Join(dir, "cwd"))
			var ep Endpoint
			var ok bool
			switch name := filepath.Base(args[0]); {
			case strings.Contains(name, "sing-box"):
				ep, ok = fromSingBox(args[1:], cwd)
			case strings.Contains(name, "mihomo"), strings.Contains(name, "clash"):
				ep, ok = fromMihomo(args[1:], cwd)
			}
			if ok {
				ep.Source = args[0] + " (pid " + strconv.Itoa(pid) + ")"
				return ep, nil
			}
		}
		return Endpoint{}, ErrNoController
	}
}

// flagValues collects the values of the given flags in both "-c x" and
// "-c=x" forms.
func flagValues(args []string, names ...string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		for _, n := range names {
			switch {
			case args[i] == n && i+1 < len(args):
				out = append(out, args[i+1])
			case strings.HasPrefix(args[i], n+"="):
				out = append(out, strings.TrimPrefix(args[i], n+"="))
			}
		}
	}
	return out
}

func abs(p, cwd string) string {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || cwd == "" {
		return p
	}
	return filepath.Join(cwd, p)
}

// fromSingBox reads experimental.clash_api from the files sing-box loads:
// every -c file and every *.json in every -C directory, or config.json in
// the working directory when neither is given.
func fromSingBox(args []string, cwd string) (Endpoint, bool) {
	var files []string
	for _, f := range flagValues(args, "-c", "--config") {
		files = append(files, abs(f, cwd))
	}
	for _, d := range flagValues(args, "-C", "--config-directory") {
		m, _ := filepath.Glob(filepath.Join(abs(d, cwd), "*.json"))
		sort.Strings(m)
		files = append(files, m...)
	}
	if len(files) == 0 {
		files = []string{abs("config.json", cwd)}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var cfg struct {
			Experimental struct {
				ClashAPI struct {
					Controller string `json:"external_controller"`
					Secret     string `json:"secret"`
				} `json:"clash_api"`
			} `json:"experimental"`
		}
		if json.Unmarshal(raw, &cfg) != nil || cfg.Experimental.ClashAPI.Controller == "" {
			continue
		}
		base, err := baseURL(cfg.Experimental.ClashAPI.Controller)
		if err != nil {
			continue
		}
		return Endpoint{Base: base, Secret: cfg.Experimental.ClashAPI.Secret}, true
	}
	return Endpoint{}, false
}

// fromMihomo reads external-controller and secret from the -f file (or
// config.yaml in the -d directory); -ext-ctl and -secret on the command
// line override them, as they do in mihomo itself.
func fromMihomo(args []string, cwd string) (Endpoint, bool) {
	var file string
	if f := flagValues(args, "-f"); len(f) > 0 {
		file = abs(f[len(f)-1], cwd)
	} else if d := flagValues(args, "-d"); len(d) > 0 {
		file = filepath.Join(abs(d[len(d)-1], cwd), "config.yaml")
	}
	var ctl, secret string
	if file != "" {
		if raw, err := os.ReadFile(file); err == nil {
			ctl, secret = yamlTop(raw, "external-controller"), yamlTop(raw, "secret")
		}
	}
	if v := flagValues(args, "-ext-ctl"); len(v) > 0 {
		ctl = v[len(v)-1]
	}
	if v := flagValues(args, "-secret"); len(v) > 0 {
		secret = v[len(v)-1]
	}
	if ctl == "" {
		return Endpoint{}, false
	}
	base, err := baseURL(ctl)
	if err != nil {
		return Endpoint{}, false
	}
	return Endpoint{Base: base, Secret: secret}, true
}

// yamlTop returns a top-level scalar from a YAML file without a YAML
// library: both keys the monitor needs are plain top-level scalars.
func yamlTop(raw []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, key+":") {
			continue // indented lines belong to nested maps
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
				return v[1 : end+1]
			}
		}
		v, _, _ = strings.Cut(v, " #")
		return strings.TrimSpace(v)
	}
	return ""
}
