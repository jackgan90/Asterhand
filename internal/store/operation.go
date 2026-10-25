package store

import (
	"errors"

	"github.com/jackgan90/Asterhand/internal/model"
)

// CompleteOperation commits the verified file evidence and operation together.
// It also repairs legacy completed operations whose job evidence was not saved.
func (s *Store) CompleteOperation(op model.Operation) error {
	if op.State != "completed" || op.Artifact == nil || op.Artifact.Operation != op.ID {
		return errors.New("无效的文件完成证据")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var recorded model.Operation
	if err = get(tx, "operation", op.ID, &recorded); err != nil {
		return err
	}
	if recorded.JobID != op.JobID || recorded.Attempt != op.Attempt || recorded.Digest != op.Digest {
		return ErrConflict
	}
	var j model.Job
	if err = get(tx, "job", op.JobID, &j); err != nil {
		return err
	}
	if j.Attempt != op.Attempt {
		return errors.New("文件证据不属于当前 attempt")
	}
	for _, artifact := range j.Artifacts {
		if artifact.Operation == op.ID {
			return nil
		}
	}
	if err = checkEventBudget(j.Seq, true); err != nil {
		return err
	}
	j.Artifacts = append(j.Artifacts, *op.Artifact)
	if err = put(tx, "operation", op.ID, op); err != nil {
		return err
	}
	if err = event(tx, &j, "artifact_verified", op.Artifact); err != nil {
		return err
	}
	return tx.Commit()
}
