package quickinput

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

func TestSetLocaleIsolatesHotkeySurfaceAndRecordingLease(t *testing.T) {
	prevPref := i18n.DefaultPreference()
	t.Cleanup(func() { i18n.SetDefaultPreference(prevPref) })

	c, r, s := controllerFixture(t)
	if _, err := c.StartRecording("page-locale-test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	recID := c.record.ID
	c.mu.Lock()
	c.status.Supported = false
	c.mu.Unlock()
	c.factory = func() (Surface, error) { return nil, errors.New("webview2 unavailable") }
	r.conflict = c.Status().Desired.Binding.Key

	updated, err := c.SetLocale(context.Background(), LocaleChange{
		Operation: "locale-isolated-save",
		Revision:  c.Status().Desired.Revision,
		Locale:    "en-US",
	})
	if err != nil {
		t.Fatalf("SetLocale failed despite isolation contract: %v", err)
	}
	if updated.Desired.Locale != "en-US" || updated.EffectiveLocale != "en-US" || updated.LastLocaleOperation != "locale-isolated-save" {
		t.Fatalf("unexpected locale status: %+v", updated)
	}
	if s.locale != "en-US" {
		t.Fatalf("ready surface did not receive locale update: got %q", s.locale)
	}
	if _, err := c.Recording(recID, "page-locale-test", false); err != nil || c.record.ID != recID {
		t.Fatalf("SetLocale cancelled active recording lease: id=%q err=%v", c.record.ID, err)
	}

	// Legacy Apply without Locale pointer preserves the saved locale.
	c.mu.Lock()
	c.status.Supported = true
	c.mu.Unlock()
	c.factory = func() (Surface, error) { return s, nil }
	r.conflict = ""
	legacyChange := Change{
		Operation: "legacy-theme-save",
		Revision:  updated.Desired.Revision,
		Enabled:   updated.Desired.Enabled,
		Binding:   updated.Desired.Binding,
		Theme:     "frost",
	}
	afterLegacy, err := c.Apply(context.Background(), legacyChange)
	if err != nil {
		t.Fatal(err)
	}
	if afterLegacy.Desired.Locale != "en-US" || afterLegacy.Desired.Theme != "frost" {
		t.Fatalf("legacy Apply lost saved locale: %+v", afterLegacy.Desired)
	}
}

func TestLegacyAndCorruptPreferencesLocaleHandling(t *testing.T) {
	prevPref := i18n.DefaultPreference()
	t.Cleanup(func() { i18n.SetDefaultPreference(prevPref) })

	dir := t.TempDir()
	path := filepath.Join(dir, "desktop-settings.json")
	legacyRaw := `{"schema_version":1,"enabled":true,"binding":{"modifiers":["ctrl","alt"],"key":"Space"},"theme_id":"star","revision":"rev-legacy"}`
	if err := os.WriteFile(path, []byte(legacyRaw), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewPreferencesStore(dir).Load()
	if err != nil || loaded.Locale != "auto" {
		t.Fatalf("legacy preferences did not default locale to auto: %+v %v", loaded, err)
	}

	corruptDir := t.TempDir()
	corruptPath := filepath.Join(corruptDir, "desktop-settings.json")
	corruptRaw := []byte("{corrupt-json")
	if err := os.WriteFile(corruptPath, corruptRaw, 0600); err != nil {
		t.Fatal(err)
	}
	broken := New(corruptDir)
	t.Cleanup(broken.Close)
	_, err = broken.SetLocale(context.Background(), LocaleChange{
		Operation: "corrupt-locale-attempt",
		Revision:  broken.Status().Desired.Revision,
		Locale:    "en-US",
	})
	var inputErr *Error
	if !errors.As(err, &inputErr) || inputErr.Status != 409 {
		t.Fatalf("expected 409 on corrupt desktop-settings.json, got %v", err)
	}
	diskAfter, err := os.ReadFile(corruptPath)
	if err != nil || string(diskAfter) != string(corruptRaw) {
		t.Fatalf("corrupt desktop-settings.json was overwritten: %q %v", string(diskAfter), err)
	}
}

func TestSetLocaleDeterministicLateRequestRaceAndIdempotency(t *testing.T) {
	prevPref := i18n.DefaultPreference()
	t.Cleanup(func() { i18n.SetDefaultPreference(prevPref) })

	c, _, _ := controllerFixture(t)
	rev0 := c.Status().Desired.Revision

	// 1. Request A (zh-CN) is prepared at rev0 and delayed in transit.
	delayedA := LocaleChange{Operation: "locale-op-delayed-a", Revision: rev0, Locale: "zh-CN"}

	// 2. Client times out waiting for A and reconciles GET /api/desktop/settings before A lands.
	reconcileSnap := c.Status()
	if reconcileSnap.Desired.Revision != rev0 || reconcileSnap.LastLocaleOperation == delayedA.Operation {
		t.Fatalf("unexpected pre-A snapshot: %+v", reconcileSnap)
	}

	// 3. User picks English; request B commits (rev0 -> rev1).
	committedB, err := c.SetLocale(context.Background(), LocaleChange{
		Operation: "locale-op-commit-b",
		Revision:  reconcileSnap.Desired.Revision,
		Locale:    "en-US",
	})
	if err != nil {
		t.Fatal(err)
	}
	rev1 := committedB.Desired.Revision
	if rev1 == rev0 || committedB.Desired.Locale != "en-US" {
		t.Fatalf("request B did not advance revision: %+v", committedB)
	}

	// 4. Delayed request A finally reaches c.SetLocale and must be rejected inside c.op.
	_, err = c.SetLocale(context.Background(), delayedA)
	var inputErr *Error
	if !errors.As(err, &inputErr) || inputErr.Status != 409 || inputErr.Key != "err.quickinput.revision_conflict" {
		t.Fatalf("expected delayed A to be rejected with 409 revision conflict, got %v", err)
	}
	afterLateA := c.Status()
	if afterLateA.Desired.Locale != "en-US" || afterLateA.Desired.Revision != rev1 || afterLateA.LastLocaleOperation != "locale-op-commit-b" {
		t.Fatalf("late A overwrote committed B: %+v", afterLateA)
	}

	// 5. Idempotent replay of B returns identical status; reusing operation_id with different locale fails 409.
	replayB, err := c.SetLocale(context.Background(), LocaleChange{
		Operation: "locale-op-commit-b",
		Revision:  rev0,
		Locale:    "en-US",
	})
	if err != nil || replayB.Desired.Revision != rev1 {
		t.Fatalf("idempotent replay failed: %+v %v", replayB, err)
	}
	_, err = c.SetLocale(context.Background(), LocaleChange{
		Operation: "locale-op-commit-b",
		Revision:  rev0,
		Locale:    "zh-CN",
	})
	if !errors.As(err, &inputErr) || inputErr.Status != 409 {
		t.Fatalf("expected reused operation_id with altered payload to fail 409, got %v", err)
	}
}
