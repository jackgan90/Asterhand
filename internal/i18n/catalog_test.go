package i18n

import (
	"errors"
	"testing"
)

func TestLocaleMatchingAndFallback(t *testing.T) {
	if got, ok := MatchLocale("zh-CN"); !ok || got != "zh-CN" {
		t.Fatalf("exact match failed: %q %v", got, ok)
	}
	if got, ok := MatchLocale("zh_Hans_SG", "en-US"); !ok || got != "zh-CN" {
		t.Fatalf("prefix match failed: %q %v", got, ok)
	}
	if got, ok := MatchLocale("en-GB"); !ok || got != "en-US" {
		t.Fatalf("english prefix match failed: %q %v", got, ok)
	}
	if got, ok := MatchLocale("fr-FR", "de-DE"); ok || got != "en-US" {
		t.Fatalf("expected fallback en-US, got %q %v", got, ok)
	}

	restore := SetSystemLocaleForTest([]string{"fr-FR"}, false)
	defer restore()
	if eff, detected := ResolveLocale("auto"); eff != "en-US" || detected {
		t.Fatalf("undetected system locale: eff=%q detected=%v", eff, detected)
	}
	if eff, detected := ResolveLocale("zh-CN"); eff != "zh-CN" || detected {
		t.Fatalf("explicit zh-CN with undetected OS: eff=%q detected=%v", eff, detected)
	}

	restoreZH := SetSystemLocaleForTest([]string{"zh-TW"}, true)
	defer restoreZH()
	if eff, detected := ResolveLocale("auto"); eff != "zh-CN" || !detected {
		t.Fatalf("detected zh-TW system locale: eff=%q detected=%v", eff, detected)
	}
}

func TestPlaceholderInterpolationAndKeyedError(t *testing.T) {
	got := T("en-US", "web.node.save_to_root", map[string]string{"root": `D:\Work`})
	if got != `Saving to D:\Work` {
		t.Fatalf("unexpected interpolation: %q", got)
	}
	// Missing parameter must preserve literal {root} without panicking.
	missing := T("en-US", "web.node.save_to_root", nil)
	if missing != "Saving to {root}" {
		t.Fatalf("missing placeholder not preserved: %q", missing)
	}

	sentinel := Err("err.store.not_found")
	wrapped := Wrap("err.env.bundle_target", map[string]string{"id": "default"}, sentinel)
	if !errors.Is(wrapped, sentinel) {
		t.Fatal("expected errors.Is to match wrapped sentinel")
	}
	msg, key, args := LocalizeError("en-US", wrapped)
	if key != "err.env.bundle_target" || args["id"] != "default" || msg == "" {
		t.Fatalf("unexpected LocalizeError: msg=%q key=%q args=%v", msg, key, args)
	}

	unknown := FromKey("err.unknown.future_key", nil, "readable fallback")
	uMsg, uKey, _ := LocalizeError("en-US", unknown)
	if uMsg != "readable fallback" || uKey != "err.unknown.future_key" {
		t.Fatalf("unknown key should fall back cleanly: msg=%q key=%q", uMsg, uKey)
	}
}
