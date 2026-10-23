package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// PendingChat is someone who wrote to the bot without being allowed.
type PendingChat struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	At       string `json:"at"` // RFC 3339, local time
}

// Pending keeps the last few rejected chats in a JSON file that the LuCI
// page reads to offer an "Allow" button.
type Pending struct {
	Path string
	Max  int // default 10

	mu sync.Mutex
}

// Add records chat, moving it to the top if it is already listed.
func (p *Pending) Add(c PendingChat) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var list []PendingChat
	if raw, err := os.ReadFile(p.Path); err == nil {
		_ = json.Unmarshal(raw, &list)
	}
	list = slices.DeleteFunc(list, func(x PendingChat) bool { return x.ID == c.ID })
	if c.At == "" {
		c.At = time.Now().Format(time.RFC3339)
	}
	list = append([]PendingChat{c}, list...)
	max := p.Max
	if max <= 0 {
		max = 10
	}
	if len(list) > max {
		list = list[:max]
	}
	raw, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o700); err != nil {
		return err
	}
	tmp := p.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.Path)
}
