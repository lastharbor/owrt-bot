package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// UCIPath is where LuCI keeps the settings. When it exists it wins over
// the INI file, which remains only for migration from the Lua bot.
const UCIPath = "/etc/config/owrt-bot"

// uciLists maps UCI list options to the comma-separated INI keys the rest
// of the code reads.
var uciLists = map[string]string{"allowed_chat_id": "allowed_chat_ids", "service": "services", "check": "checks", "group": "groups"}

// FromUCIShow converts `uci -q show owrt-bot` output into the INI shape,
// so FromINI stays the single place that applies defaults and validation.
//
//	owrt-bot.telegram=telegram
//	owrt-bot.telegram.bot_token='123:abc'
//	owrt-bot.telegram.allowed_chat_id='111' '222'
func FromUCIShow(text string) INI {
	ini := INI{}
	for _, line := range strings.Split(text, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		parts := strings.SplitN(key, ".", 3)
		if len(parts) != 3 {
			continue // section declaration
		}
		sec, opt := parts[1], parts[2]
		if ini[sec] == nil {
			ini[sec] = map[string]string{}
		}
		vals := splitUCIValues(val)
		if ini2, isList := uciLists[opt]; isList {
			ini[sec][ini2] = strings.Join(vals, ",")
			continue
		}
		ini[sec][opt] = strings.Join(vals, " ")
	}
	return ini
}

// splitUCIValues reads uci's quoting: values in single quotes separated
// by spaces; an embedded quote is closed quote, backslash-quote, reopen.
func splitUCIValues(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'':
			inQuote = !inQuote
			have = true
		case ch == '\\' && !inQuote && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case ch == ' ' && !inQuote:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(ch)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func uciQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// uciSections fixes the file's section order; anything else is appended.
var uciSections = []string{"telegram", "alerts", "logging", "bot"}

// RenderUCI writes ini as /etc/config/owrt-bot. The [wol] section is left
// out (devices live in wol.json) and so is [general]: the language is
// chosen in the chat.
func RenderUCI(ini INI) string {
	var b strings.Builder
	seen := map[string]bool{"wol": true, "general": true}
	write := func(sec string) {
		opts, ok := ini[sec]
		// Core sections are always written: LuCI's NamedSection form shows
		// nothing for a section that does not exist.
		if seen[sec] || (!ok && !slices.Contains(uciSections, sec)) {
			return
		}
		seen[sec] = true
		fmt.Fprintf(&b, "config %s '%s'\n", sec, sec)
		keys := make([]string, 0, len(opts))
		for k := range opts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "allowed_chat_ids" {
				for _, id := range strings.Split(opts[k], ",") {
					if id = strings.TrimSpace(id); id != "" {
						fmt.Fprintf(&b, "\tlist allowed_chat_id %s\n", uciQuote(id))
					}
				}
				continue
			}
			if k == "mode" || k == "platform" || k == "webhook_url" || k == "webhook_port" {
				continue // Lua-bot options with no meaning here
			}
			fmt.Fprintf(&b, "\toption %s %s\n", k, uciQuote(opts[k]))
		}
		b.WriteString("\n")
	}
	for _, s := range uciSections {
		write(s)
	}
	rest := make([]string, 0)
	for s := range ini {
		rest = append(rest, s)
	}
	sort.Strings(rest)
	for _, s := range rest {
		write(s)
	}
	return b.String()
}
