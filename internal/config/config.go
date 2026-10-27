// Package config loads the bot configuration from an INI-style file.
//
// The format is the same one the old Lua bot used, so an existing
// /etc/owrt-tg-bot/config.conf can be copied over unchanged:
//
//	[telegram]
//	bot_token=123:abc
//	allowed_chat_ids=111,222
//
// Unknown keys are ignored; missing keys fall back to defaults.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// INI is the raw parsed file: section -> key -> value.
type INI map[string]map[string]string

// ParseINI reads key=value pairs grouped by [section]. Lines starting
// with ';' or '#' are comments. A single pair of surrounding quotes is
// stripped from values.
func ParseINI(text string) INI {
	out := INI{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if out[section] == nil {
				out[section] = map[string]string{}
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			continue
		}
		out[section][strings.TrimSpace(key)] = unquote(strings.TrimSpace(val))
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func (i INI) str(section, key, def string) string {
	if v, ok := i[section][key]; ok && v != "" {
		return v
	}
	return def
}

func (i INI) num(section, key string, def float64) float64 {
	if v, err := strconv.ParseFloat(i.str(section, key, ""), 64); err == nil {
		return v
	}
	return def
}

// Telegram holds API access settings.
type Telegram struct {
	Token        string
	AllowedChats []int64
	APIBase      string // https://api.telegram.org
}

// Alerts holds background threshold-monitor settings.
type Alerts struct {
	Enabled     bool
	CPUPercent  float64
	MemPercent  float64
	DiskPercent float64
	TempCelsius float64
	Interval    time.Duration
	WANHost     string
	WANFailNeed int
	WANOkNeed   int
}

// VPNWatch checks proxy groups through the Clash API (mihomo, sing-box).
type VPNWatch struct {
	Enabled    bool
	Interval   time.Duration
	FailNeed   int
	OkNeed     int
	DefaultURL string
	Checks     map[string]string // group -> URL of the service it carries
	Groups     []string          // groups to check; empty = every top-level group
	Controller string            // Clash API address; empty = find a running engine
	Secret     string
}

// Paths groups the files the bot owns.
type Paths struct {
	DataDir string // wol.json, prefs.json, ssh key, known_hosts
	LogFile string
}

// Config is the fully resolved configuration.
type Config struct {
	Telegram     Telegram
	Language     string
	Alerts       Alerts
	VPNWatch     VPNWatch
	Paths        Paths
	LogMaxKB     int
	LogLevel     string
	LANIface     string
	DNSServer    string // optional "127.0.0.1:53"; empty = system resolver
	LegacyWOL    map[string]string
	SpeedtestURL string
	SetupPort    int // LAN port for PC setup scripts

	ini INI
}

// Raw returns a value the typed fields do not cover ("" if absent).
func (c *Config) Raw(section, key string) string { return c.ini.str(section, key, "") }

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return FromINI(ParseINI(string(raw)))
}

// FromINI builds a Config with defaults applied and validates it.
func FromINI(ini INI) (*Config, error) {
	c := &Config{
		Telegram: Telegram{
			Token:   ini.str("telegram", "bot_token", ""),
			APIBase: ini.str("telegram", "api_base", "https://api.telegram.org"),
		},
		Language: ini.str("general", "language", "ru"),
		Alerts: Alerts{
			Enabled:     ini.str("alerts", "enabled", "1") != "0",
			CPUPercent:  ini.num("alerts", "cpu_percent", 90),
			MemPercent:  ini.num("alerts", "mem_percent", 90),
			DiskPercent: ini.num("alerts", "disk_percent", 90),
			TempCelsius: ini.num("alerts", "temp_celsius", 80),
			Interval:    time.Duration(ini.num("alerts", "check_interval_sec", 60)) * time.Second,
			WANHost:     ini.str("alerts", "wan_check_host", "1.1.1.1"),
			WANFailNeed: int(ini.num("alerts", "wan_fail_need", 3)),
			WANOkNeed:   int(ini.num("alerts", "wan_ok_need", 3)),
		},
		VPNWatch: VPNWatch{
			Enabled:    ini.str("vpnwatch", "enabled", "1") != "0",
			Interval:   time.Duration(ini.num("vpnwatch", "interval_sec", 300)) * time.Second,
			FailNeed:   int(ini.num("vpnwatch", "fail_need", 2)),
			OkNeed:     int(ini.num("vpnwatch", "ok_need", 2)),
			DefaultURL: ini.str("vpnwatch", "default_url", "https://www.gstatic.com/generate_204"),
			Checks:     map[string]string{},
			Controller: ini.str("vpnwatch", "controller", ""),
			Secret:     ini.str("vpnwatch", "secret", ""),
		},
		Paths: Paths{
			DataDir: ini.str("bot", "data_dir", "/etc/owrt-bot"),
			LogFile: ini.str("logging", "file", "/var/log/owrt-bot.log"),
		},
		LogMaxKB:     int(ini.num("logging", "max_size_kb", 256)),
		LogLevel:     ini.str("logging", "level", "info"),
		LANIface:     ini.str("bot", "lan_iface", ""), // empty = network.lan.device
		DNSServer:    ini.str("bot", "dns_server", ""),
		SpeedtestURL: ini.str("bot", "speedtest_url", ""), // empty = built-in server list
		SetupPort:    int(ini.num("bot", "setup_port", 8099)),
		LegacyWOL:    ini["wol"],
		ini:          ini,
	}
	for _, s := range strings.Split(ini.str("telegram", "allowed_chat_ids", ""), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("allowed_chat_ids: bad id %q", s)
		}
		c.Telegram.AllowedChats = append(c.Telegram.AllowedChats, id)
	}
	if c.Telegram.Token == "" {
		return nil, fmt.Errorf("[telegram] bot_token is empty")
	}
	// No allowed chats is valid: the bot answers nobody but remembers who
	// wrote, so the owner can allow them from LuCI.
	if c.Alerts.Interval < 10*time.Second {
		c.Alerts.Interval = 10 * time.Second
	}
	// "section=url" pairs; a URL may itself contain "=", so cut once.
	for _, s := range strings.Split(ini.str("vpnwatch", "checks", ""), ",") {
		name, u, ok := strings.Cut(strings.TrimSpace(s), "=")
		if !ok || name == "" || u == "" {
			continue
		}
		c.VPNWatch.Checks[strings.TrimSpace(name)] = strings.TrimSpace(u)
	}
	for _, g := range strings.Split(ini.str("vpnwatch", "groups", ""), ",") {
		if g = strings.TrimSpace(g); g != "" {
			c.VPNWatch.Groups = append(c.VPNWatch.Groups, g)
		}
	}
	if c.VPNWatch.Interval < time.Minute {
		c.VPNWatch.Interval = time.Minute
	}
	c.VPNWatch.FailNeed = max(c.VPNWatch.FailNeed, 1)
	c.VPNWatch.OkNeed = max(c.VPNWatch.OkNeed, 1)
	return c, nil
}
