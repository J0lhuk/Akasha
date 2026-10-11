package syncer

import (
	"context"
	"encoding/json"
	"errors"

	"akasha/internal/core"
	"akasha/internal/vault"
)

type Envelope struct {
	Version    int    `json:"version"`
	SpaceID    string `json:"space_id"`
	ProjectID  string `json:"project_id"`
	ID         string `json:"id"`
	Ciphertext []byte `json:"ciphertext"`
}
type RemoteEvent struct {
	Sequence int64    `json:"sequence"`
	Envelope Envelope `json:"envelope"`
}
type Page struct {
	Events []RemoteEvent `json:"events"`
	Head   int64         `json:"head"`
}
type Enrollment struct {
	Space   core.Space   `json:"space"`
	Project core.Project `json:"project"`
}

func associated(e Envelope) string {
	return "akasha/sync/v1/" + e.SpaceID + "/" + e.ProjectID + "/" + e.ID
}
func Encrypt(key vault.Key, p core.Project, r core.Record) (Envelope, error) {
	e := Envelope{Version: 1, SpaceID: p.SpaceID, ProjectID: p.ID, ID: r.ID}
	data, err := json.Marshal(r)
	if err != nil {
		return e, err
	}
	e.Ciphertext, err = vault.Seal(key, associated(e), data)
	return e, err
}
func Decrypt(key vault.Key, p core.Project, e Envelope) (core.Record, error) {
	if e.Version != 1 || e.SpaceID != p.SpaceID || e.ProjectID != p.ID || core.ValidateID(e.ID) != nil {
		return core.Record{}, errors.New("encrypted event scope mismatch")
	}
	data, err := vault.Open(key, associated(e), e.Ciphertext)
	if err != nil {
		return core.Record{}, err
	}
	defer clear(data)
	var r core.Record
	if err = json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.ID != e.ID {
		return r, errors.New("event id does not match encrypted record")
	}
	return r, r.Validate()
}
func saveWire(ctx context.Context, s *core.Store, scope string, e Envelope) (Envelope, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	actual, err := s.OnceState(ctx, "wire/"+scope+"/"+e.ID, string(b))
	if err != nil {
		return e, err
	}
	err = json.Unmarshal([]byte(actual), &e)
	return e, err
}
