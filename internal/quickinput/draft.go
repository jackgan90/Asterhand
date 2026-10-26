package quickinput

import (
	"fmt"
	"sync"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

const MaxDraftBytes = hostipc.MaxTextBytes

type Draft struct {
	mu       sync.Mutex
	text     string
	revision uint64
	epoch    uint64
}

func (d *Draft) Read() (string, uint64) { d.mu.Lock(); defer d.mu.Unlock(); return d.text, d.revision }
func (d *Draft) Update(text string, revision uint64) error {
	if !hostipc.ValidText(text) {
		return fmt.Errorf("草稿需要有效 Unicode、LF 换行且不超过 16,000 UTF-8 字节")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if revision <= d.revision {
		if revision == d.revision && text == d.text {
			return nil
		}
		return fmt.Errorf("忽略过期草稿版本")
	}
	d.text, d.revision = text, revision
	return nil
}
func (d *Draft) Clear() { d.mu.Lock(); d.text = ""; d.revision++; d.epoch++; d.mu.Unlock() }

// state and accept are the parent-authoritative, cross-process draft contract.
// epoch starts at one on the wire even for a zero-value Draft.
func (d *Draft) state() (text string, revision, epoch uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.text, d.revision, d.epoch + 1
}

func (d *Draft) accept(text string, epoch uint64) (uint64, error) {
	if !hostipc.ValidText(text) {
		return 0, hostipc.ErrProtocol
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if epoch != d.epoch+1 {
		return 0, hostipc.ErrProtocol
	}
	if text != d.text {
		d.text = text
		d.revision++
	}
	return d.revision, nil
}
