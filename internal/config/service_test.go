package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFreshServiceStartLocksBeforeCreatingIdentity(t *testing.T) {
	home := filepath.Join(t.TempDir(), "节点 数据")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "launch-state.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		cfg     *Manager
		release func()
		err     error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			m, release, err := OpenLocked(home)
			results <- result{m, release, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	id := ""
	for r := range results {
		if r.err == nil {
			winners++
			id = r.cfg.Get().NodeID
			defer r.release()
		}
	}
	if winners != 1 {
		t.Fatal("fresh starts must have one owner", winners)
	}
	m, err := Open(home)
	if err != nil || m.Get().NodeID != id {
		t.Fatal("concurrent starts changed identity", err)
	}
}
