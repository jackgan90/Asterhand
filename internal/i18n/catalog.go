// Package i18n provides embedded runtime translation catalogs and locale matching.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

//go:embed locales/*
var localeFS embed.FS

// LocaleEntry describes one registered runtime UI locale in manifest.json.
type LocaleEntry struct {
	ID                string   `json:"id"`
	Label             string   `json:"label"`
	HTMLLang          string   `json:"html_lang"`
	Prefixes          []string `json:"prefixes"`
	ShowBrandSubtitle bool     `json:"show_brand_subtitle"`
	DisallowCJK       bool     `json:"disallow_cjk,omitempty"`
}

// Manifest is the machine-readable locale registry loaded from locales/manifest.json.
type Manifest struct {
	Schema             int           `json:"schema"`
	DefaultFallback    string        `json:"default_fallback"`
	WebPrefixes        []string      `json:"web_prefixes"`
	QuickInputPrefixes []string      `json:"quickinput_prefixes"`
	Locales            []LocaleEntry `json:"locales"`
}

var (
	paramPattern = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)
	bundleOnce   sync.Once
	manifestData Manifest
	catalogData  map[string]map[string]string

	stateMu           sync.RWMutex
	defaultPreference = "auto"
	testSysOverride   *sysOverride
)

type sysOverride struct {
	langs    []string
	detected bool
}

func loadBundle() {
	bundleOnce.Do(func() {
		raw, err := localeFS.ReadFile("locales/manifest.json")
		if err != nil {
			panic(fmt.Sprintf("i18n: read manifest.json: %v", err))
		}
		if err := json.Unmarshal(raw, &manifestData); err != nil {
			panic(fmt.Sprintf("i18n: parse manifest.json: %v", err))
		}
		catalogData = make(map[string]map[string]string, len(manifestData.Locales))
		for _, entry := range manifestData.Locales {
			b, err := localeFS.ReadFile("locales/" + entry.ID + ".json")
			if err != nil {
				panic(fmt.Sprintf("i18n: read %s.json: %v", entry.ID, err))
			}
			var table map[string]string
			if err := json.Unmarshal(b, &table); err != nil {
				panic(fmt.Sprintf("i18n: parse %s.json: %v", entry.ID, err))
			}
			catalogData[entry.ID] = table
		}
	})
}

// Registry returns a copy of the loaded locale manifest.
func Registry() Manifest {
	loadBundle()
	out := manifestData
	out.Locales = append([]LocaleEntry(nil), manifestData.Locales...)
	return out
}

// DefaultFallback returns the ultimate fallback locale ID from manifest.json.
func DefaultFallback() string {
	loadBundle()
	return manifestData.DefaultFallback
}

// Locales returns all registered locales in manifest order.
func Locales() []LocaleEntry {
	loadBundle()
	return append([]LocaleEntry(nil), manifestData.Locales...)
}

// Entry looks up a registered locale entry case-insensitively.
func Entry(id string) (LocaleEntry, bool) {
	loadBundle()
	norm := normalizeTag(id)
	for _, entry := range manifestData.Locales {
		if strings.EqualFold(entry.ID, norm) {
			return entry, true
		}
	}
	return LocaleEntry{}, false
}

// ValidLocale reports whether id is an exact registered locale ID.
func ValidLocale(id string) bool {
	loadBundle()
	for _, entry := range manifestData.Locales {
		if entry.ID == id {
			return true
		}
	}
	return false
}

// ValidPreference reports whether v is "auto" or a registered locale ID.
func ValidPreference(v string) bool {
	return v == "auto" || ValidLocale(v)
}

func normalizeTag(tag string) string {
	s := strings.TrimSpace(tag)
	if idx := strings.IndexAny(s, ".@"); idx >= 0 {
		s = s[:idx]
	}
	return strings.ReplaceAll(s, "_", "-")
}

// MatchLocale matches ordered language candidates against manifest.json using:
// 1) case-insensitive exact ID match; 2) language family prefix match; 3) default_fallback.
func MatchLocale(candidates ...string) (string, bool) {
	loadBundle()
	return MatchLocaleAgainst(manifestData, candidates...)
}

// MatchLocaleAgainst runs the 3-tier locale matching rule against an explicit Manifest.
func MatchLocaleAgainst(m Manifest, candidates ...string) (string, bool) {
	for _, raw := range candidates {
		norm := normalizeTag(raw)
		if norm == "" || strings.EqualFold(norm, "c") || strings.EqualFold(norm, "posix") {
			continue
		}
		for _, entry := range m.Locales {
			if strings.EqualFold(entry.ID, norm) {
				return entry.ID, true
			}
		}
		primary := norm
		if idx := strings.IndexByte(norm, '-'); idx >= 0 {
			primary = norm[:idx]
		}
		for _, entry := range m.Locales {
			for _, prefix := range entry.Prefixes {
				if strings.EqualFold(prefix, primary) || strings.EqualFold(prefix, norm) {
					return entry.ID, true
				}
			}
		}
	}
	return m.DefaultFallback, false
}

// SystemLanguages returns the ordered OS UI language tags and whether detection succeeded.
func SystemLanguages() ([]string, bool) {
	stateMu.RLock()
	override := testSysOverride
	stateMu.RUnlock()
	if override != nil {
		return append([]string(nil), override.langs...), override.detected
	}
	return detectSystemLanguages()
}

// SetSystemLocaleForTest overrides OS UI language detection for deterministic tests.
func SetSystemLocaleForTest(langs []string, detected bool) func() {
	stateMu.Lock()
	prev := testSysOverride
	cp := append([]string(nil), langs...)
	testSysOverride = &sysOverride{langs: cp, detected: detected}
	stateMu.Unlock()
	return func() {
		stateMu.Lock()
		testSysOverride = prev
		stateMu.Unlock()
	}
}

// SetDefaultPreference updates the process-wide locale preference ("auto" or locale ID).
func SetDefaultPreference(preference string) {
	if preference == "" || !ValidPreference(preference) {
		preference = "auto"
	}
	stateMu.Lock()
	defaultPreference = preference
	stateMu.Unlock()
}

// DefaultPreference returns the current process-wide locale preference.
func DefaultPreference() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return defaultPreference
}

// ResolveLocale resolves a preference ("auto" or locale ID) to the node's effective locale
// and reports whether the OS UI language was recognized in the registry.
func ResolveLocale(preference string) (effective string, systemDetected bool) {
	loadBundle()
	langs, ok := SystemLanguages()
	sysLocale, matched := MatchLocale(langs...)
	systemDetected = ok && matched
	if preference != "" && preference != "auto" {
		if entry, valid := Entry(preference); valid {
			return entry.ID, systemDetected
		}
	}
	if systemDetected {
		return sysLocale, true
	}
	return manifestData.DefaultFallback, false
}

// Effective returns the node's current effective locale based on DefaultPreference().
func Effective() string {
	eff, _ := ResolveLocale(DefaultPreference())
	return eff
}

// HasKey reports whether key exists in the translation catalog.
func HasKey(key string) bool {
	loadBundle()
	if key == "" {
		return false
	}
	_, ok := catalogData[manifestData.DefaultFallback][key]
	return ok
}

// T formats key in locale (falling back to DefaultFallback and preserving missing {param} tokens).
func T(locale, key string, args ...map[string]string) string {
	loadBundle()
	resolved, _ := MatchLocale(locale)
	template, ok := catalogData[resolved][key]
	if !ok {
		template, ok = catalogData[manifestData.DefaultFallback][key]
		if !ok {
			return key
		}
	}
	var params map[string]string
	if len(args) > 0 {
		params = args[0]
	}
	if len(params) == 0 || strings.IndexByte(template, '{') < 0 {
		return template
	}
	return paramPattern.ReplaceAllStringFunc(template, func(token string) string {
		name := token[1 : len(token)-1]
		if val, exists := params[name]; exists {
			return val
		}
		return token
	})
}
