package bot

import (
	"strings"

	"github.com/lastharbor/owrt-bot/internal/telegram"
)

// Callback data is "<feature>:<action>[:arg...]". Telegram caps it at 64
// bytes, so arguments should be short ids, never names. MAC addresses go
// through CompactMAC because ':' is the separator.

// Button is an inline keyboard button.
type Button = telegram.InlineButton

// Route joins a route and its arguments into callback data.
func Route(route string, args ...string) string {
	if len(args) == 0 {
		return route
	}
	return route + ":" + strings.Join(args, ":")
}

// Btn makes an inline button that triggers route with args.
func Btn(text, route string, args ...string) telegram.InlineButton {
	return telegram.InlineButton{Text: text, CallbackData: Route(route, args...)}
}

// KB is a fluent inline-keyboard builder.
type KB struct {
	rows [][]telegram.InlineButton
}

func NewKB() *KB { return &KB{} }

// Row appends one row; empty rows are skipped.
func (k *KB) Row(btns ...telegram.InlineButton) *KB {
	if len(btns) > 0 {
		k.rows = append(k.rows, btns)
	}
	return k
}

// Grid lays buttons out cols per row.
func (k *KB) Grid(cols int, btns ...telegram.InlineButton) *KB {
	for i := 0; i < len(btns); i += cols {
		k.Row(btns[i:min(i+cols, len(btns))]...)
	}
	return k
}

// RowIf appends a row only when cond holds.
func (k *KB) RowIf(cond bool, btns ...telegram.InlineButton) *KB {
	if cond {
		k.Row(btns...)
	}
	return k
}

func (k *KB) Build() *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{Rows: k.rows}
}

// CompactMAC turns "02:5E:10:AA:BB:01" into "025e10aabb01" for callbacks.
func CompactMAC(mac string) string {
	return strings.ToLower(strings.ReplaceAll(mac, ":", ""))
}

// ExpandMAC reverses CompactMAC; malformed input is returned unchanged.
func ExpandMAC(s string) string {
	if len(s) != 12 {
		return s
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(s[i : i+2])
	}
	return b.String()
}
