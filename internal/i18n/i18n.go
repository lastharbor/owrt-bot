// Package i18n holds UI strings. Every feature reads text through a
// Translator, so adding a language means adding one more Catalog.
package i18n

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// Catalog maps message keys to templates with {name} placeholders.
type Catalog map[string]string

// Translator resolves keys in the active language, falling back to the
// base catalog, and finally to the key itself (a visible placeholder
// beats a crash).
type Translator struct {
	catalogs map[string]Catalog
	names    map[string]string
	base     string
	current  atomic.Value // string
}

// New registers catalogs; base is the fallback language code.
func New(base string) *Translator {
	t := &Translator{catalogs: map[string]Catalog{}, names: map[string]string{}, base: base}
	t.current.Store(base)
	return t
}

// Add registers a language with its human-readable name.
func (t *Translator) Add(code, name string, c Catalog) *Translator {
	t.catalogs[code] = c
	t.names[code] = name
	return t
}

// Use switches the active language; unknown codes are ignored.
func (t *Translator) Use(code string) bool {
	if _, ok := t.catalogs[code]; !ok {
		return false
	}
	t.current.Store(code)
	return true
}

// Lang returns the active language code.
func (t *Translator) Lang() string { return t.current.Load().(string) }

// Languages lists registered codes in stable order with their names.
func (t *Translator) Languages() (codes []string, names map[string]string) {
	for c := range t.catalogs {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes, t.names
}

// T renders key with alternating name/value pairs:
//
//	t.T("wol_woke", "name", "HomePC", "time", "23 s")
func (t *Translator) T(key string, kv ...any) string {
	tpl, ok := t.catalogs[t.Lang()][key]
	if !ok {
		if tpl, ok = t.catalogs[t.base][key]; !ok {
			return key
		}
	}
	if len(kv) == 0 {
		return tpl
	}
	pairs := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, "{"+fmt.Sprint(kv[i])+"}", fmt.Sprint(kv[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(tpl)
}
