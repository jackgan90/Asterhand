package config

import (
	"os"
	"path/filepath"
)

// OpenLocked acquires the process lock before initial identity/configuration
// creation. Concurrent fresh starts must never create different node identities.
func OpenLocked(home string) (*Manager, func(), error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, nil, err
	}
	if err = validateHome(abs); err != nil {
		return nil, nil, err
	}
	if err = privateDirectory(abs); err != nil {
		return nil, nil, err
	}
	unlock, err := Lock(abs)
	if err != nil {
		return nil, nil, err
	}
	c, err := Open(abs)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return c, unlock, nil
}
