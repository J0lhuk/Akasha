package core

import (
	"context"
	"database/sql"
	"errors"
)

// Local features deliberately live outside the replicated event journal.
// A downloaded project must not enable optional agent behavior on a new host.
func (s *Store) FeatureEnabled(ctx context.Context, name string) (bool, error) {
	if err := ValidateID(name); err != nil {
		return false, err
	}
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM state WHERE key=?", "feature/"+name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if value != "true" && value != "false" {
		return false, errors.New("invalid local feature setting")
	}
	return value == "true", nil
}

// Callers must obtain a Store with write authorization before changing a flag.
func (s *Store) SetFeature(ctx context.Context, name string, enabled bool) error {
	if err := ValidateID(name); err != nil {
		return err
	}
	value := "false"
	if enabled {
		value = "true"
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "feature/"+name, value)
	return err
}
