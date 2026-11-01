package store

import (
	"database/sql"
	"errors"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
)

// Definitive refusal is a durable outcome for this request identity. Returning
// it before commit would allow an older, delayed submission to execute later.
func recordRejection(tx *sql.Tx, key string, request model.Request, err error) error {
	var rejection *model.TaskRejection
	if !errors.As(err, &rejection) {
		return err
	}
	if !rejection.Matches(request) || rejection.Message == "" {
		return i18n.Err("err.store.rejection_mismatch")
	}
	if err := put(tx, "task-rejection", key, rejection); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return rejection
}
