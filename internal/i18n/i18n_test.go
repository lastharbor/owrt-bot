package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCatalogsHaveSameKeys(t *testing.T) {
	for k := range RU {
		if _, ok := EN[k]; !ok {
			t.Errorf("EN is missing %q", k)
		}
	}
	for k := range EN {
		if _, ok := RU[k]; !ok {
			t.Errorf("RU is missing %q", k)
		}
	}
}

// Every literal key passed to T(...) in the code base must exist, so a
// typo shows up here instead of as a raw key on a button.
func TestCodeKeysExist(t *testing.T) {
	re := regexp.MustCompile(`\b[Tt]\("([a-z0-9_]+)"`)
	root := filepath.Join("..")
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, _ := os.ReadFile(path)
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if _, ok := EN[m[1]]; !ok {
				t.Errorf("%s: unknown key %q", path, m[1])
			}
		}
		return nil
	})
}

func TestTranslate(t *testing.T) {
	tr := New("en").Add("en", "English", Catalog{"hi": "Hi {name}", "only_en": "x"}).
		Add("ru", "Русский", Catalog{"hi": "Привет, {name}"})
	if got := tr.T("hi", "name", "Антон"); got != "Hi Антон" {
		t.Errorf("got %q", got)
	}
	tr.Use("ru")
	if got := tr.T("hi", "name", "Антон"); got != "Привет, Антон" {
		t.Errorf("got %q", got)
	}
	if tr.T("only_en") != "x" || tr.T("missing") != "missing" {
		t.Error("fallback broken")
	}
}
