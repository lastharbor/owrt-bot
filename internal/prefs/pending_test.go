package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPendingKeepsNewestUniqueChats(t *testing.T) {
	p := &Pending{Path: filepath.Join(t.TempDir(), "sub", "pending.json"), Max: 2}
	for _, c := range []PendingChat{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 1, Name: "A again"}, {ID: 3, Name: "C"}} {
		if err := p.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	var got []PendingChat
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 1 || got[1].Name != "A again" || got[0].At == "" {
		t.Fatalf("pending = %+v", got)
	}
}
