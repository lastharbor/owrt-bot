package config

import (
	"strings"
	"testing"
)

func TestFromUCIShow(t *testing.T) {
	text := `owrt-bot.telegram=telegram
owrt-bot.telegram.bot_token='123:abc'
owrt-bot.telegram.allowed_chat_id='111' '222'
owrt-bot.alerts=alerts
owrt-bot.alerts.cpu_percent='85'
owrt-bot.bot=bot
owrt-bot.bot.services='A:a,B:b:bb'
owrt-bot.bot.note='it'\''s'
`
	ini := FromUCIShow(text)
	c, err := FromINI(ini)
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.Token != "123:abc" || len(c.Telegram.AllowedChats) != 2 || c.Telegram.AllowedChats[1] != 222 {
		t.Errorf("telegram = %+v", c.Telegram)
	}
	if c.Alerts.CPUPercent != 85 || c.Raw("bot", "services") != "A:a,B:b:bb" {
		t.Errorf("alerts/services wrong: %v %q", c.Alerts.CPUPercent, c.Raw("bot", "services"))
	}
	if got := ini["bot"]["note"]; got != "it's" {
		t.Errorf("quote unescape: %q", got)
	}
}

func TestRenderUCIRoundTrip(t *testing.T) {
	ini := ParseINI(`[telegram]
bot_token=123:abc
allowed_chat_ids=111,222
mode="polling"
[general]
language="ru"
[alerts]
cpu_percent="85"
[wol]
pc1="X|02:5e:10:aa:bb:01"
`)
	out := RenderUCI(ini)
	for _, want := range []string{"config telegram 'telegram'", "list allowed_chat_id '111'", "option cpu_percent '85'"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"mode", "[wol]", "pc1", "general"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, out)
		}
	}
}
