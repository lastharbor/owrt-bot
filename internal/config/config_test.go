package config

import (
	"testing"
	"time"
)

const sample = `
[telegram]
bot_token=123:abc
allowed_chat_ids= 11, 22
mode="polling"

; comment
[alerts]
cpu_percent="85"
check_interval_sec=5

[wol]
pc1="HomePC|02:5e:10:aa:bb:01|br-lan|192.168.1.121"
`

func TestFromINI(t *testing.T) {
	c, err := FromINI(ParseINI(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.Token != "123:abc" {
		t.Errorf("token = %q", c.Telegram.Token)
	}
	if len(c.Telegram.AllowedChats) != 2 || c.Telegram.AllowedChats[1] != 22 {
		t.Errorf("chats = %v", c.Telegram.AllowedChats)
	}
	if c.Alerts.CPUPercent != 85 {
		t.Errorf("cpu = %v", c.Alerts.CPUPercent)
	}
	if c.Alerts.Interval != 10*time.Second {
		t.Errorf("interval should be clamped to 10s, got %v", c.Alerts.Interval)
	}
	if c.LegacyWOL["pc1"] != "HomePC|02:5e:10:aa:bb:01|br-lan|192.168.1.121" {
		t.Errorf("legacy wol = %v", c.LegacyWOL)
	}
	if c.Language != "ru" {
		t.Errorf("language default = %q", c.Language)
	}
}

func TestFromINIRejectsEmptyChats(t *testing.T) {
	if _, err := FromINI(ParseINI("[telegram]\nbot_token=x\n")); err == nil {
		t.Fatal("expected error for empty allowed_chat_ids")
	}
}
