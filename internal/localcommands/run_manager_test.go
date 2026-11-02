package localcommands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeExecution struct {
	kind                 string
	out, err             *io.PipeReader
	outWriter, errWriter *io.PipeWriter
	done                 chan struct{}
	once                 sync.Once
	code                 uint32
	stops                atomic.Int32
}

func newFakeExecution() *fakeExecution {
	f := &fakeExecution{done: make(chan struct{})}
	f.out, f.outWriter = io.Pipe()
	f.err, f.errWriter = io.Pipe()
	return f
}
func (f *fakeExecution) Stdout() io.ReadCloser { return f.out }
func (f *fakeExecution) Stderr() io.ReadCloser { return f.err }
func (f *fakeExecution) ForceStop() error      { f.stops.Add(1); f.complete(); return nil }
func (f *fakeExecution) complete() {
	f.once.Do(func() { close(f.done); f.outWriter.Close(); f.errWriter.Close() })
}
func (f *fakeExecution) Wait(ctx context.Context) (ExitEvidence, error) {
	select {
	case <-ctx.Done():
		return ExitEvidence{}, ctx.Err()
	case <-f.done:
		code := f.code
		kind := f.kind
		if kind == "" {
			kind = "exited"
		}
		return ExitEvidence{Kind: kind, Code: &code}, nil
	}
}

type alternativePolicy struct{ windowsCommandPolicy }

func (alternativePolicy) EqualPath(a, b string) bool              { return a == b }
func (alternativePolicy) ScriptPath(value string) (string, error) { return filepath.Clean(value), nil }
func (alternativePolicy) ValidArgument(string, string) bool       { return true }

func TestManagedPolicyAndAbnormalExitEvidence(t *testing.T) {
	r := OpenWithPolicy(t.TempDir(), nil, alternativePolicy{})
	c := testCommand(t, "case_one")
	c.Script = filepath.Join(filepath.Dir(c.Script), "script.sh")
	c.Arguments = []string{`a"b`}
	first := saveCommand(t, r, c)
	c.Name = "case_two"
	c.Script = strings.ToUpper(c.Script)
	second := saveCommand(t, r, c)
	if second.ID == first.ID || second.Merged {
		t.Fatal("case-sensitive policy was ignored")
	}
	_, m, b, _, command := managedFixture(t)
	if _, err := m.Start(command); err != nil {
		t.Fatal(err)
	}
	e := <-b.created
	e.kind = "signal"
	e.complete()
	if record := awaitFinished(t, m, command.ID); record.Result != "failed" {
		t.Fatal("abnormal zero evidence reported success", record)
	}
}
func (f *fakeExecution) Close() error { f.out.Close(); return f.err.Close() }

type fakeBackend struct {
	created chan *fakeExecution
	count   atomic.Int32
	empty   atomic.Bool
}

func (f *fakeBackend) Available() bool { return true }
func (f *fakeBackend) Start(Command, string) (Execution, error) {
	e := newFakeExecution()
	f.count.Add(1)
	f.created <- e
	return e, nil
}
func (f *fakeBackend) Reconcile(string) (bool, error) { return f.empty.Load(), nil }

type fakeClock struct {
	now  atomic.Int64
	wake chan time.Time
}

func (c *fakeClock) Now() time.Duration { return time.Duration(c.now.Load()) }
func (c *fakeClock) Wake(ctx context.Context) <-chan time.Time {
	ch := make(chan time.Time)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-c.wake:
				select {
				case ch <- t:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

func managedFixture(t *testing.T) (*Registry, *RunManager, *fakeBackend, *fakeClock, Command) {
	t.Helper()
	home := t.TempDir()
	b := &fakeBackend{created: make(chan *fakeExecution, 16)}
	b.empty.Store(true)
	clock := &fakeClock{wake: make(chan time.Time, 4)}
	m := NewRunManager(home, b, clock)
	r := Open(home, nil)
	if err := r.ConfigureManaged(m); err != nil {
		t.Fatal(err)
	}
	c := testCommand(t, "managed")
	c.ExecutionMode = "managed"
	saved := saveCommand(t, r, c)
	c = saved.Catalog.Commands[0]
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return r, m, b, clock, c
}
func awaitRun(t *testing.T, m *RunManager, id string, predicate func(*RunRecord) bool) *RunRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r := m.Current(id)
		if r != nil && predicate(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not settle: %+v", r)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func awaitFinished(t *testing.T, m *RunManager, id string) *RunRecord {
	return awaitRun(t, m, id, func(r *RunRecord) bool { return r.Phase == "finished" })
}

func TestManagedSingleInstanceDrainLogAndRestart(t *testing.T) {
	r, m, b, _, c := managedFixture(t)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.Launch(context.Background(), c.ID, c.Revision).Started {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 || b.count.Load() != 1 {
		t.Fatalf("accepted=%d starts=%d", accepted.Load(), b.count.Load())
	}
	e := <-b.created
	if _, err := r.Delete(c.ID, mustSnapshot(t, r).Revision); err == nil {
		t.Fatal("deleted running command")
	}
	if _, err := r.Save(Mutation{Revision: mustSnapshot(t, r).Revision, Command: c}); err == nil {
		t.Fatal("edited running command")
	}
	stdout := strings.Repeat("中文 without newline ", 10000)
	stderr := strings.Repeat("err\x00", 5000)
	wg.Add(2)
	go func() { defer wg.Done(); io.WriteString(e.outWriter, stdout) }()
	go func() { defer wg.Done(); io.WriteString(e.errWriter, stderr) }()
	wg.Wait()
	e.complete()
	result := awaitFinished(t, m, c.ID)
	if result.Result != "succeeded" || !result.LogComplete {
		t.Fatalf("%+v", result)
	}
	got := map[string]string{}
	cursor := int64(0)
	for {
		page, err := m.Read(c.ID, result.ID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range page.Chunks {
			got[chunk.Stream] += string(chunk.Data)
		}
		cursor = page.Next
		if page.End {
			break
		}
	}
	if got["stdout"] != stdout || got["stderr"] != stderr {
		t.Fatal("lost or reordered output")
	}
	tail, err := m.Read(c.ID, result.ID, -1)
	if err != nil || tail.Start == 0 {
		t.Fatal("tail", tail.Start, err)
	}
	reopened := NewRunManager(filepath.Dir(m.store.root), b, nil)
	if reopened.loadErr != nil {
		t.Fatal(reopened.loadErr)
	}
	if got := reopened.Current(c.ID); got.Result != result.Result || got.ID != result.ID {
		t.Fatal(got)
	}
}
func mustSnapshot(t *testing.T, r *Registry) Catalog {
	t.Helper()
	c, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestManagedStopTimeoutGenerationAndConfigurationCleanup(t *testing.T) {
	r, m, b, clock, c := managedFixture(t)
	seconds := int64(2)
	c.TimeoutSeconds = &seconds
	c = saveCommand(t, r, c).Catalog.Commands[0]
	first, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	e := <-b.created
	io.WriteString(e.outWriter, "before stop")
	if _, err = m.Stop(c.ID, first.ID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	result := awaitFinished(t, m, c.ID)
	if result.Result != "interrupted" || result.Reason != "user_stop" {
		t.Fatal(result)
	}
	if _, err = m.Stop(c.ID, first.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	second, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	next := <-b.created
	awaitRun(t, m, c.ID, func(r *RunRecord) bool { return r.Phase == "running" })
	if _, err = m.Stop(c.ID, first.ID, "user_stop"); err == nil {
		t.Fatal("old stop accepted")
	}
	if _, err = m.Read(c.ID, first.ID, 0); err == nil {
		t.Fatal("old read accepted")
	}
	clock.now.Store(int64(3 * time.Second))
	clock.wake <- time.Now()
	result = awaitFinished(t, m, c.ID)
	if result.ID != second.ID || result.Result != "failed" || result.Reason != "timeout" || next.stops.Load() != 1 {
		t.Fatal(result)
	}
	c.ExecutionMode = "unmanaged"
	saved := saveCommand(t, r, c)
	if saved.Catalog.Commands[0].TimeoutSeconds != nil || m.Current(c.ID) != nil {
		t.Fatal("mode change kept run")
	}
	if _, err = os.Stat(m.store.directory(c.ID)); !os.IsNotExist(err) {
		t.Fatal("run directory remains", err)
	}
}

func TestManagedRecoveryBlocksUntilScopeConfirmed(t *testing.T) {
	home := t.TempDir()
	b := &fakeBackend{created: make(chan *fakeExecution, 1)}
	m := NewRunManager(home, b, nil)
	c := testCommand(t, "recover")
	c.ID = "command1234567890"
	c.ExecutionMode = "managed"
	record := RunRecord{ID: "execution123456789", Command: c, Phase: "stopping", Reason: "timeout", LogComplete: true}
	f, err := m.store.prepare(record)
	if err != nil {
		t.Fatal(err)
	}
	appendLog(f, 0, []byte("durable"))
	f.Close()
	recovered := NewRunManager(home, b, nil)
	if !recovered.Busy(c.ID) {
		t.Fatal("unconfirmed scope released")
	}
	if _, err = recovered.Start(c); err == nil {
		t.Fatal("restarted unknown scope")
	}
	b.empty.Store(true)
	result := awaitFinished(t, recovered, c.ID)
	if result.Result != "failed" || result.Reason != "timeout" || result.LogComplete {
		t.Fatal(result)
	}
}

func TestManagedFailedPreparePreservesPrevious(t *testing.T) {
	_, m, b, _, c := managedFixture(t)
	first, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	e := <-b.created
	e.complete()
	awaitFinished(t, m, c.ID)
	original := m.store.root
	m.store.root = filepath.Join(original, "blocked")
	if err = os.WriteFile(m.store.root, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Start(c); err == nil {
		t.Fatal("prepare unexpectedly succeeded")
	}
	m.store.root = original
	if m.Current(c.ID).ID != first.ID {
		t.Fatal("failed prepare replaced result")
	}
}

func TestManagedLegacySchemaMigrationAndValidation(t *testing.T) {
	home := t.TempDir()
	r := Open(home, nil)
	c := testCommand(t, "legacy")
	saved := saveCommand(t, r, c)
	path := r.path
	b, _ := os.ReadFile(path)
	b = []byte(strings.Replace(string(b), `"schema_version": 3`, `"schema_version": 1`, 1))
	os.WriteFile(path, b, 0600)
	reopened := Open(home, nil)
	catalog := mustSnapshot(t, reopened)
	if catalog.Schema != 3 || catalog.Commands[0].ExecutionMode != "unmanaged" || catalog.Commands[0].Kind != "script" {
		t.Fatal(catalog)
	}
	c = saved.Catalog.Commands[0]
	c.ExecutionMode = "managed"
	zero := int64(0)
	c.TimeoutSeconds = &zero
	if _, err := normalize(c); err == nil {
		t.Fatal("zero timeout accepted")
	}
}
