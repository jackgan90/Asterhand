package quickinput

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

const MaxDraftBytes = 16000

type Draft struct {
	mu       sync.Mutex
	text     string
	revision uint64
}

func (d *Draft) Read() (string, uint64) { d.mu.Lock(); defer d.mu.Unlock(); return d.text, d.revision }
func (d *Draft) Update(text string, revision uint64) error {
	if !utf8.ValidString(text) || len(text) > MaxDraftBytes || strings.Contains(text, "\r") {
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
func (d *Draft) Clear() { d.mu.Lock(); d.text = ""; d.revision++; d.mu.Unlock() }
