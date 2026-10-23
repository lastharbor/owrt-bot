// Package prefs stores settings changed from the chat (not the config
// file, which stays hand-edited and is never rewritten by the bot).
package prefs

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

type data struct {
	Language string `json:"language,omitempty"`
}

// File is a tiny JSON-backed preference store.
type File struct {
	path string
	mu   sync.Mutex
	d    data
}

func Open(path string) (*File, error) {
	f := &File{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return f, json.Unmarshal(raw, &f.d)
}

func (f *File) Language() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.d.Language
}

func (f *File) SetLanguage(code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.d.Language = code
	raw, _ := json.MarshalIndent(f.d, "", "  ")
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}
