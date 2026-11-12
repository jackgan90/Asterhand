package store

import (
	"database/sql"
	"errors"

	"github.com/jackgan90/Asterhand/internal/model"
)

func (s *Store) RequestJob(origin, request string) (model.Job, error) {
	var id string
	if err := s.db.QueryRow("SELECT job FROM dedup WHERE origin=? AND request=?", origin, request).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Job{}, ErrNotFound
		}
		return model.Job{}, err
	}
	var job model.Job
	err := s.Get("job", id, &job)
	return job, err
}
