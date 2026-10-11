package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrDenied   = errors.New("access denied")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("version conflict: reload or resolve all alternatives")
	safeID      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
)

type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}
type Project struct {
	ID      string `json:"id"`
	SpaceID string `json:"space_id"`
	Name    string `json:"name"`
	Root    string `json:"root"`
	Shared  bool   `json:"shared"`
}
type Principal struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}
type Grant struct {
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}
type Link struct {
	Target   string `json:"target"`
	Relation string `json:"relation"`
}

// Record is an immutable revision. Parents are the exact revisions superseded by it.
// Raw code, facts and history are never replaced with their compressed presentations.
type Record struct {
	ID        string            `json:"id"`
	Entity    string            `json:"entity"`
	Kind      string            `json:"kind"`
	Title     string            `json:"title"`
	Text      string            `json:"text,omitempty"`
	Source    string            `json:"source,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Links     []Link            `json:"links,omitempty"`
	Parents   []string          `json:"parents"`
	Author    string            `json:"author"`
	Created   string            `json:"created"`
	Deleted   bool              `json:"deleted,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Predicate string            `json:"predicate,omitempty"`
	Value     string            `json:"value,omitempty"`
	ValidFrom string            `json:"valid_from,omitempty"`
	ValidTo   string            `json:"valid_to,omitempty"`
	Blob      []byte            `json:"blob,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}
type Conflict struct {
	Entity   string   `json:"entity"`
	Kind     string   `json:"kind"`
	Versions []Record `json:"versions"`
}
type WriteInput struct {
	Entity string   `json:"entity"`
	Kind   string   `json:"kind"`
	Title  string   `json:"title"`
	Text   string   `json:"text"`
	Source string   `json:"source"`
	Tags   []string `json:"tags,omitempty"`
	Links  []Link   `json:"links,omitempty"`
	// Updates require the exact current heads. nil creates a new entity or fact alternative.
	Expected  []string          `json:"expected,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Predicate string            `json:"predicate,omitempty"`
	Value     string            `json:"value,omitempty"`
	ValidFrom string            `json:"valid_from,omitempty"`
	ValidTo   string            `json:"valid_to,omitempty"`
	Deleted   bool              `json:"deleted,omitempty"`
	Blob      []byte            `json:"blob,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b)
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func ValidateID(id string) error {
	if !safeID.MatchString(id) {
		return fmt.Errorf("invalid id")
	}
	return nil
}
func Now() string               { return time.Now().UTC().Format(time.RFC3339Nano) }
func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
func decodeRecord(data string) (Record, error) {
	var r Record
	err := json.Unmarshal([]byte(data), &r)
	return r, err
}
func searchable(kind string) bool {
	switch kind {
	case "note", "code", "symbol", "fact", "prompt", "checkpoint", "decision", "task", "tool":
		return true
	}
	return false
}
func validKind(kind string) bool {
	return searchable(kind) || kind == "file" || kind == "secret" || kind == "git_bundle" || kind == "prompt_template" || kind == "context_policy" || kind == "skill_bundle" || kind == "ilang_skill" || kind == "ilang_run"
}
func (r Record) Validate() error {
	if ValidateID(r.ID) != nil || r.Entity == "" || len(r.Entity) > 600 || !validKind(r.Kind) || len(r.Title) > 1000 || len(r.Text) > 2<<20 || len(r.Blob) > 32<<20 || len(r.Parents) > 1000 || len(r.Links) > 2000 || len(r.Tags) > 100 {
		return errors.New("invalid record or record exceeds limits")
	}
	if len(r.Source) > 4000 || len(r.Subject) > 1000 || len(r.Predicate) > 1000 || len(r.Value) > 16000 || len(r.Meta) > 64 || len(r.Author) > 200 {
		return errors.New("record metadata exceeds limits")
	}
	for k, v := range r.Meta {
		if len(k) > 200 || len(v) > 16000 {
			return errors.New("record metadata field exceeds limits")
		}
	}
	for _, v := range r.Tags {
		if len(v) > 200 {
			return errors.New("tag exceeds limit")
		}
	}
	for _, v := range r.Links {
		if len(v.Target) > 1000 || len(v.Relation) > 100 {
			return errors.New("link exceeds limit")
		}
	}
	if searchable(r.Kind) && len(r.Blob) > 0 {
		return errors.New("searchable records cannot contain binary payloads")
	}
	if _, err := time.Parse(time.RFC3339Nano, r.Created); err != nil {
		return errors.New("invalid record timestamp")
	}
	for _, p := range r.Parents {
		if ValidateID(p) != nil || p == r.ID {
			return errors.New("invalid parent revision")
		}
	}
	if r.Kind == "secret" && (r.Text != "" || len(r.Blob) == 0) {
		return errors.New("secret record must contain ciphertext only")
	}
	if r.Kind == "fact" && (r.Subject == "" || r.Predicate == "" || r.Value == "") {
		return errors.New("fact requires subject, predicate and value")
	}
	if r.Kind == "fact" && r.Entity != "fact/"+Hash(normalize(r.Subject)+"\x00"+normalize(r.Predicate)) {
		return errors.New("fact identity mismatch")
	}
	for _, s := range []string{r.ValidFrom, r.ValidTo} {
		if s != "" {
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				return errors.New("fact validity requires RFC3339")
			}
		}
	}
	if r.ValidFrom != "" && r.ValidTo != "" {
		f, _ := time.Parse(time.RFC3339, r.ValidFrom)
		t, _ := time.Parse(time.RFC3339, r.ValidTo)
		if !f.Before(t) {
			return errors.New("invalid validity interval")
		}
	}
	return nil
}
