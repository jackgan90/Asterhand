package quickinput

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testRegistrar struct {
	next        int
	held        map[int]Binding
	conflict    string
	failRelease map[int]bool
	onRegister  func(int)
}

func (r *testRegistrar) Register(b Binding) (int, error) {
	if b.Key == r.conflict {
		return 0, &RegistrationError{"conflict", "occupied"}
	}
	r.next++
	r.held[r.next] = b
	if r.onRegister != nil {
		r.onRegister(r.next)
	}
	return r.next, nil
}
func (r *testRegistrar) Unregister(id int) error {
	if r.failRelease[id] {
		return errors.New("release failed")
	}
	delete(r.held, id)
	return nil
}

type testSurface struct {
	toggles     int
	closed      bool
	snapshotErr error
	theme       string
}

func (s *testSurface) Toggle()                        { s.toggles++ }
func (s *testSurface) Update(theme string) error      { s.theme = theme; return nil }
func (s *testSurface) Snapshot(context.Context) error { return s.snapshotErr }
func (s *testSurface) Close() error                   { s.closed = true; return nil }
func controllerFixture(t *testing.T) (*Controller, *testRegistrar, *testSurface) {
	t.Helper()
	c := New(t.TempDir())
	c.status.Supported = true
	r := &testRegistrar{held: map[int]Binding{}, failRelease: map[int]bool{}}
	s := &testSurface{}
	c.registrar, c.factory = r, func() (Surface, error) { s.closed = false; return s, nil }
	if _, err := c.Retry(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, r, s
}
func changed(c *Controller, key string) Change {
	p := c.Status().Desired
	return Change{Operation: "operation-" + key + p.Revision, Revision: p.Revision, Enabled: p.Enabled, Binding: Binding{[]string{"ctrl", "alt"}, key}, Theme: p.Theme}
}

func TestTransactionConflictPersistenceAndIdempotency(t *testing.T) {
	c, r, s := controllerFixture(t)
	before := c.Status()
	old := c.active
	r.conflict = "KeyK"
	if _, err := c.Apply(context.Background(), changed(c, "KeyK")); err == nil {
		t.Fatal("conflict accepted")
	}
	if !reflect.DeepEqual(before.Desired, c.Status().Desired) || c.active != old {
		t.Fatal("old state changed")
	}
	c.OnHotkey(old)
	if s.toggles != 1 {
		t.Fatal("old binding stopped working")
	}
	r.conflict = ""
	change := changed(c, "KeyL")
	r.onRegister = func(id int) { c.OnHotkey(id) }
	after, err := c.Apply(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	if s.toggles != 1 || len(r.held) != 1 || c.active == old {
		t.Fatal("candidate triggered or leaked")
	}
	saved, err := c.store.Load()
	if err != nil || !reflect.DeepEqual(saved, after.Desired) {
		t.Fatal("persistence mismatch", err)
	}
	replayed, err := c.Apply(context.Background(), change)
	if err != nil || !reflect.DeepEqual(replayed, after) {
		t.Fatal("idempotent replay changed result", err)
	}
	change.Theme = "frost"
	if _, err := c.Apply(context.Background(), change); err == nil {
		t.Fatal("operation ID reuse accepted")
	}
	change.Operation = "different-operation"
	if _, err := c.Apply(context.Background(), change); err == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestWriteFailureAndReleaseFailureKeepAuthority(t *testing.T) {
	c, r, _ := controllerFixture(t)
	before := c.Status().Desired
	old := c.active
	store := c.store
	c.store = NewPreferencesStore(filepath.Join(c.home, "missing"))
	if _, err := c.Apply(context.Background(), changed(c, "KeyL")); err == nil {
		t.Fatal("write failure accepted")
	}
	if !reflect.DeepEqual(before, c.Status().Desired) || c.active != old || len(r.held) != 1 {
		t.Fatal("rollback lost old state")
	}
	c.store = store
	r.failRelease[old] = true
	after, err := c.Apply(context.Background(), changed(c, "KeyM"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Desired.Binding.Key != "KeyM" || after.CleanupPending != 1 || c.active == old {
		t.Fatal("commit incorrectly rolled back")
	}
	if err := c.Check(DefaultBinding()); err == nil || len(r.held) != 2 {
		t.Fatal("cleanup guard failed")
	}
	c.OnHotkey(old)
	if c.surface.(*testSurface).toggles != 0 {
		t.Fatal("retired ID triggered")
	}
	disable := changed(c, "KeyM")
	disable.Enabled = false
	if _, err = c.Apply(context.Background(), disable); err != nil {
		t.Fatal("cannot disable during cleanup", err)
	}
	delete(r.failRelease, old)
}

func TestRecordingLeaseDraftAndDisableSnapshot(t *testing.T) {
	c, _, s := controllerFixture(t)
	if _, err := c.StartRecording("page-a", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartRecording("page-b", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("second recorder accepted")
	}
	id := c.record.ID
	c.OnHotkey(c.active)
	if s.toggles != 0 || c.record.Binding == nil {
		t.Fatal("owned binding was not recorded")
	}
	if _, err := c.Recording(id, "page-b", true); err == nil {
		t.Fatal("another page cancelled recorder")
	}
	c.mu.Lock()
	c.record.Expires = time.Now().Add(-time.Second)
	c.mu.Unlock()
	c.OnHotkey(c.active)
	if s.toggles != 1 {
		t.Fatal("expired lease still suppresses binding")
	}
	s.snapshotErr = errors.New("IME composing")
	disable := changed(c, "KeyJ")
	disable.Enabled = false
	if _, err := c.Apply(context.Background(), disable); err == nil || !c.Status().Desired.Enabled {
		t.Fatal("unconfirmed draft was closed")
	}
	s.snapshotErr = nil
	disable.Operation = "disable-success"
	if _, err := c.Apply(context.Background(), disable); err != nil || !s.closed {
		t.Fatal(err)
	}
}

func TestCorruptConfigurationNeedsExplicitRecovery(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "desktop-settings.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	c := New(home)
	c.status.Supported = true
	r := &testRegistrar{held: map[int]Binding{}, failRelease: map[int]bool{}}
	c.registrar, c.factory = r, func() (Surface, error) { return &testSurface{}, nil }
	defer c.Close()
	if _, err := c.Retry(); err == nil || len(r.held) != 0 {
		t.Fatal("invalid configuration silently replaced")
	}
	change := changed(c, "KeyJ")
	change.Reset = true
	if _, err := c.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".recovery-*")
	if len(backups) != 1 {
		t.Fatal("missing original backup")
	}
	original, _ := os.ReadFile(backups[0])
	if string(original) != `{"schema_version":99}` {
		t.Fatal("backup modified")
	}
}

func TestDraftUTF8LimitsAndVersion(t *testing.T) {
	for _, text := range []string{strings.Repeat("a", 16000), strings.Repeat("中", 5333), strings.Repeat("😀", 4000), "空格 \t\n"} {
		d := &Draft{}
		if err := d.Update(text, 1); err != nil {
			t.Fatal(err)
		}
		if err := d.Update(text+"😀", 2); len(text) > 15990 && err == nil {
			t.Fatal("overflow accepted")
		}
		if err := d.Update("old", 0); err == nil {
			t.Fatal("stale draft accepted")
		}
	}
	for _, text := range []string{"\xff", "a\rb", strings.Repeat("中", 5334), strings.Repeat("😀", 4001)} {
		if err := new(Draft).Update(text, 1); err == nil {
			t.Fatal("invalid draft accepted")
		}
	}
}

func TestNoopAndCancelledChange(t *testing.T) {
	c, r, _ := controllerFixture(t)
	before := c.Status().Desired
	if _, err := c.Apply(context.Background(), changed(c, "KeyJ")); err != nil {
		t.Fatal(err)
	}
	if c.Status().Desired.Revision != before.Revision || r.next != 1 {
		t.Fatal("healthy no-op created a transaction")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Apply(ctx, changed(c, "KeyK")); err == nil || r.next != 1 {
		t.Fatal("cancelled change registered")
	}
	c.BeginStop()
	if _, err := c.Apply(context.Background(), changed(c, "KeyL")); err == nil {
		t.Fatal("accepted after stop")
	}
}

func TestStartupConflictAndSurfaceFailureRecovery(t *testing.T) {
	c := New(t.TempDir())
	c.status.Supported = true
	defer c.Close()
	r := &testRegistrar{held: map[int]Binding{}, failRelease: map[int]bool{}, conflict: "KeyJ"}
	s := &testSurface{}
	c.registrar, c.factory = r, func() (Surface, error) { return s, nil }
	if _, err := c.Retry(); err == nil || c.Status().Registration != "conflict" || c.Status().Effective != nil {
		t.Fatal("startup conflict was hidden")
	}
	r.conflict = ""
	if _, err := c.Retry(); err != nil {
		t.Fatal(err)
	}
	if err := c.draft.Update("已确认草稿", 1); err != nil {
		t.Fatal(err)
	}
	c.op.Lock()
	c.failSurface(errors.New("renderer failure"))
	c.op.Unlock()
	if len(r.held) != 0 || c.Status().Surface != "failed" {
		t.Fatal("failed surface retained active hotkey")
	}
	if _, err := c.Retry(); err != nil {
		t.Fatal(err)
	}
	if text, _ := c.draft.Read(); text != "已确认草稿" || c.Status().RecoveryNotice == "" {
		t.Fatal("recovery lost draft or notice")
	}
	loaded := New(c.home)
	if !reflect.DeepEqual(loaded.Status().Desired, c.Status().Desired) {
		t.Fatal("restart changed local preferences")
	}
	loaded.Close()
}

func TestBindingNormalizationAndReservedKeys(t *testing.T) {
	b, err := (Binding{[]string{"ALT", "ctrl", "alt"}, "KeyJ"}).Normalize()
	if err != nil || !b.Equal(DefaultBinding()) || b.String() != "Ctrl+Alt+J" {
		t.Fatal(b, err)
	}
	for _, b := range []Binding{{nil, "KeyJ"}, {[]string{"shift"}, "KeyJ"}, {[]string{"win"}, "KeyJ"}, {[]string{"ctrl"}, "F12"}, {[]string{"ctrl", "alt"}, "Delete"}, {[]string{"alt"}, "Tab"}, {[]string{"alt"}, "F4"}, {[]string{"ctrl"}, "Semicolon"}} {
		if _, err := b.Normalize(); err == nil {
			t.Fatal("unsupported combination accepted", b)
		}
	}
}

func TestStopDuringInitializationNeverRegistersLateHotkey(t *testing.T) {
	c := New(t.TempDir())
	c.status.Supported = true
	defer c.Close()
	r := &testRegistrar{held: map[int]Binding{}, failRelease: map[int]bool{}}
	s := &testSurface{}
	entered, finish := make(chan struct{}), make(chan struct{})
	c.registrar, c.factory = r, func() (Surface, error) { close(entered); <-finish; return s, nil }
	done := make(chan error, 1)
	go func() { _, err := c.Retry(); done <- err }()
	<-entered
	c.BeginStop()
	close(finish)
	if err := <-done; err == nil || r.next != 0 || !s.closed {
		t.Fatal("late initialization registered or retained the surface", err, r.next, s.closed)
	}
}

func TestTrayRecoveryRestoresSavedTheme(t *testing.T) {
	c, _, _ := controllerFixture(t)
	change := changed(c, "KeyJ")
	change.Theme = "frost"
	if _, err := c.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	c.op.Lock()
	c.failSurface(errors.New("renderer failure"))
	c.op.Unlock()
	var replacement *testSurface
	c.factory = func() (Surface, error) { replacement = &testSurface{theme: "star"}; return replacement, nil }
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	if replacement.theme != "frost" || replacement.toggles != 1 {
		t.Fatal("tray recovery did not show the saved theme", replacement)
	}
}

func TestNoopReturnsUpdatedCleanupStatus(t *testing.T) {
	c, r, _ := controllerFixture(t)
	id, err := r.Register(Binding{[]string{"ctrl", "alt"}, "KeyK"})
	if err != nil {
		t.Fatal(err)
	}
	c.retired = append(c.retired, id)
	c.mu.Lock()
	c.status.CleanupPending = 1
	c.mu.Unlock()
	before := c.Status().Desired
	result, err := c.Apply(context.Background(), changed(c, "KeyJ"))
	if err != nil {
		t.Fatal(err)
	}
	if result.CleanupPending != 0 || c.Status().CleanupPending != 0 || result.Desired.Revision != before.Revision {
		t.Fatal("no-op response did not reflect completed cleanup", result)
	}
}
