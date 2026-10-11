package core

import (
	"context"
	"fmt"
)

// CheckpointDirty is called by the daemon periodically and during graceful shutdown.
// It snapshots only changed projects; a failed snapshot is reported and retried.
func (m *Manager) CheckpointDirty(ctx context.Context) []error {
	m.mu.Lock()
	stores := make([]*Store, 0, len(m.stores))
	for _, s := range m.stores {
		stores = append(stores, s)
	}
	m.mu.Unlock()
	errs := []error{}
	for _, s := range stores {
		if m.DenseAvailable() {
			if _, e := m.indexStoreVectors(ctx, s); e != nil {
				errs = append(errs, fmt.Errorf("vectors project %s: %w", s.Project.ID, e))
			}
		}
		rev, err := s.Revision(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		old, err := s.State(ctx, "checkpoint_revision")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		current := fmt.Sprint(rev)
		if old == current {
			continue
		}
		_, err = s.Checkpoint(ctx, "automatic")
		if err == nil {
			_, err = s.ExportWiki(ctx)
		}
		if err == nil {
			err = s.SetState(ctx, "checkpoint_revision", current)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", s.Project.ID, err))
		}
	}
	return errs
}
