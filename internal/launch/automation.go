package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/operations"
)

// ErrStartReceiptRollback indicates that the pre-launch permission check failed
// before spawning the node, AND rolling back the newly written prepared receipt
// on disk also failed.
var ErrStartReceiptRollback = errors.New("start_receipt_rollback_failed")

var writeStartReceipts = saveStartReceipts

// FindInstallation binds an installed node to its existing fixed launcher
// record. Development binaries do not acquire an arbitrary executable override.
func FindInstallation(executable, home string) string {
	dir := filepath.Dir(executable)
	for range 6 {
		path := filepath.Join(dir, "installation.json")
		installation, err := ReadInstallation(path)
		if err == nil && samePath(installation.Data, home) && samePath(installation.Executable, executable) {
			return path
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

type startReceipts struct {
	Version int                  `json:"version"`
	Items   []operations.Receipt `json:"items"`
}

// StartOperation uses the installer's launch mutex and existing process/health
// checks. A prepared receipt is written before spawning; an old unknown request
// only probes, never calls launchLocked a second time.
func StartOperation(ctx context.Context, path, home string, identity operations.Identity, check func() error) (operations.Receipt, error) {
	if identity.Name != "node_start" {
		return operations.Receipt{}, operations.ErrInvalid
	}
	if err := operations.CheckName(identity.ID, identity.Name); err != nil {
		return operations.Receipt{}, err
	}
	installation, err := ReadInstallation(path)
	if err != nil {
		return operations.Receipt{}, err
	}
	if !samePath(installation.Data, home) {
		return operations.Receipt{}, operations.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	unlock, err := acquireLaunchLock(ctx, home)
	if err != nil {
		return operations.Receipt{}, err
	}
	defer unlock()
	if err = check(); err != nil {
		return operations.Receipt{}, err
	}
	var cfg struct {
		Node string `json:"node_id"`
	}
	if err = readJSON(filepath.Join(home, "config.json"), &cfg); err != nil {
		return operations.Receipt{}, err
	}
	if cfg.Node != identity.Node {
		return operations.Receipt{}, operations.ErrConflict
	}
	book, err := readStartReceipts(home)
	if err != nil {
		return operations.Receipt{}, err
	}
	index := -1
	for i, item := range book.Items {
		if item.Key() == identity.Key() {
			index = i
			break
		}
	}
	if index >= 0 {
		receipt := book.Items[index]
		if receipt.Identity != identity {
			return receipt, operations.ErrConflict
		}
		if receipt.Terminal() {
			return receipt, nil
		}
		probe, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		if _, err = existingURL(probe, home); err != nil {
			return receipt, nil
		}
		return completeStart(home, book, index)
	}
	if err = operations.CheckFresh(identity.ID, time.Now()); err != nil {
		return operations.Receipt{}, err
	}
	retained := book.Items[:0]
	for _, item := range book.Items {
		if !item.Terminal() || time.Since(time.UnixMilli(item.Updated)) <= operations.Retention {
			retained = append(retained, item)
		}
	}
	book.Items = retained
	if len(book.Items) >= 256 {
		return operations.Receipt{}, operations.ErrCapacity
	}
	receipt := operations.Receipt{Identity: identity, State: "prepared", Created: time.Now().UnixMilli(), Updated: time.Now().UnixMilli()}
	book.Items = append(book.Items, receipt)
	index = len(book.Items) - 1
	if err = writeStartReceipts(home, book); err != nil {
		return operations.Receipt{}, err
	}
	if err = check(); err != nil {
		book.Items = book.Items[:index]
		if rbErr := writeStartReceipts(home, book); rbErr != nil {
			return receipt, fmt.Errorf("%w: %v (pre-launch check failed: %w)", ErrStartReceiptRollback, rbErr, err)
		}
		return operations.Receipt{}, err
	}
	r := runner{timeout: 20 * time.Second, poll: 200 * time.Millisecond, start: startNode}
	if _, err = r.launchLocked(path, installation); err != nil {
		return receipt, err
	}
	return completeStart(home, book, index)
}

func completeStart(home string, book startReceipts, index int) (operations.Receipt, error) {
	r := &book.Items[index]
	r.State, r.Accepted, r.Updated = "completed", true, time.Now().UnixMilli()
	r.Result, _ = json.Marshal(map[string]any{"running": true, "node_id": r.Node})
	return *r, saveStartReceipts(home, book)
}

func ReadStartReceipt(home, actor, node, id string) (operations.Receipt, error) {
	book, err := readStartReceipts(home)
	if err != nil {
		return operations.Receipt{}, err
	}
	key := (operations.Identity{Actor: actor, Node: node, ID: id}).Key()
	for _, item := range book.Items {
		if item.Key() == key {
			return item, nil
		}
	}
	return operations.Receipt{}, operations.ErrMissing
}
func readStartReceipts(home string) (startReceipts, error) {
	book := startReceipts{Version: 1}
	b, err := os.ReadFile(filepath.Join(home, "automation-start.json"))
	if errors.Is(err, os.ErrNotExist) {
		return book, nil
	}
	if err != nil {
		return book, err
	}
	if len(b) > 512<<10 {
		return book, operations.ErrCapacity
	}
	if err = json.Unmarshal(b, &book); err != nil {
		return book, err
	}
	if book.Version != 1 || len(book.Items) > 256 {
		return book, operations.ErrConflict
	}
	return book, nil
}
func saveStartReceipts(home string, book startReceipts) error {
	b, err := json.Marshal(book)
	if err != nil {
		return err
	}
	if len(b) > 512<<10 {
		return operations.ErrCapacity
	}
	return config.AtomicFile(filepath.Join(home, "automation-start.json"), b, 0600)
}
