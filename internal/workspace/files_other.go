//go:build !windows

package workspace

import "os"

type heldFile struct {
	file    *os.File
	name    string
	removed bool
	parents []*os.File
	staging string
	missing []string
}

func (h *heldFile) close()                               {}
func (h *heldFile) bytes(int64) ([]byte, error)          { return nil, fail("unsupported_platform") }
func (h *heldFile) write([]byte) error                   { return fail("unsupported_platform") }
func (h *heldFile) pinAvailableParents() error           { return fail("unsupported_platform") }
func (h *heldFile) remove() error                        { return fail("unsupported_platform") }
func (h *heldFile) observe() (string, bool, error)       { return "", false, fail("unsupported_platform") }
func hold(string, string, bool, bool) (*heldFile, error) { return nil, fail("unsupported_platform") }
func directory(string, string) (*heldFile, error)        { return nil, fail("unsupported_platform") }
func platformSupported() bool                            { return false }
