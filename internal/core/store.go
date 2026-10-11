package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

type Store struct {
	Project      Project
	Path         string
	db           *sql.DB
	mu           sync.Mutex
	vectorOnce   sync.Once
	vectorError  error
	vectorMu     sync.Mutex
	vectorLoadMu sync.Mutex
}

const headPredicate = `NOT EXISTS(SELECT 1 FROM parents p WHERE p.parent=r.id)`

func OpenStore(path string, project Project) (*Store, error) {
	db, err := OpenDB(path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS records(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,entity TEXT NOT NULL,kind TEXT NOT NULL,title TEXT NOT NULL,body TEXT NOT NULL,created TEXT NOT NULL,deleted INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS records_entity ON records(entity);
 CREATE INDEX IF NOT EXISTS records_kind ON records(kind);
 CREATE TABLE IF NOT EXISTS parents(child TEXT NOT NULL REFERENCES records(id),parent TEXT NOT NULL,PRIMARY KEY(child,parent));
 CREATE INDEX IF NOT EXISTS parent_lookup ON parents(parent);
 CREATE VIRTUAL TABLE IF NOT EXISTS search USING fts5(id UNINDEXED,title,text,tags,tokenize='unicode61 remove_diacritics 2');
 CREATE TABLE IF NOT EXISTS cache(key TEXT PRIMARY KEY,revision INTEGER NOT NULL,value TEXT NOT NULL,created TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS state(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Prompt history has its own API. Keeping it out of the evidence index also
	// prevents frequent prompts from changing BM25 corpus statistics and caches.
	var indexVersion string
	err = db.QueryRow("SELECT value FROM state WHERE key='evidence-index-version'").Scan(&indexVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, err
	}
	if indexVersion != "2" {
		tx, e := db.Begin()
		if e != nil {
			db.Close()
			return nil, e
		}
		_, e = tx.Exec("DELETE FROM search WHERE id IN (SELECT id FROM records WHERE kind IN ('prompt','tool')); DELETE FROM cache; INSERT OR REPLACE INTO state(key,value) VALUES('evidence-index-version','2')")
		if e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			db.Close()
			return nil, e
		}
	}
	return &Store{Project: project, Path: path, db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Dir() string  { return filepath.Dir(s.Path) }
func (s *Store) Revision(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM records").Scan(&n)
	return n, err
}

// History and encrypted file traffic do not change retrieval evidence.
func (s *Store) KnowledgeRevision(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM records WHERE kind NOT IN ('prompt','tool','secret','file','git_bundle','prompt_template','context_policy','skill_bundle','ilang_skill','ilang_run')").Scan(&n)
	return n, err
}
func (s *Store) Knowledge(ctx context.Context, limit int) ([]Record, error) {
	if limit < 1 || limit > 10000 {
		limit = 500
	}
	return readRows(ctx, s.db, "SELECT r.body FROM records r WHERE r.deleted=0 AND r.kind NOT IN ('prompt','tool','checkpoint','secret','file','git_bundle','prompt_template','context_policy','skill_bundle','ilang_skill','ilang_run') AND "+headPredicate+" ORDER BY r.created DESC,r.id LIMIT ?", limit)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readRows(ctx context.Context, q queryer, sqlText string, args ...any) ([]Record, error) {
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		r, e := decodeRecord(body)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Heads(ctx context.Context, entity string) ([]Record, error) {
	return readRows(ctx, s.db, "SELECT r.body FROM records r WHERE r.entity=? AND "+headPredicate+" ORDER BY r.created,r.id", entity)
}
func (s *Store) List(ctx context.Context, kind string, limit int) ([]Record, error) {
	if limit < 1 || limit > 10000 {
		limit = 500
	}
	return readRows(ctx, s.db, "SELECT r.body FROM records r WHERE r.deleted=0 AND (?='' OR r.kind=?) AND "+headPredicate+" ORDER BY r.created DESC,r.id LIMIT ?", kind, kind, limit)
}

// ListNamed avoids materializing unrelated payloads for exact symbol lookup.
func (s *Store) ListNamed(ctx context.Context, kind, title string, limit int) ([]Record, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("named record limit must be 1..1000")
	}
	return readRows(ctx, s.db, "SELECT r.body FROM records r WHERE r.deleted=0 AND r.kind=? AND (?='' OR r.title=?) AND "+headPredicate+" ORDER BY r.created DESC,r.id LIMIT ?", kind, title, title, limit)
}

func (s *Store) Current(ctx context.Context, kind string, limit int) ([]Record, error) {
	if limit < 1 || limit > 10000 {
		limit = 10000
	}
	return readRows(ctx, s.db, "SELECT r.body FROM records r WHERE (?='' OR r.kind=?) AND "+headPredicate+" ORDER BY r.created DESC,r.id LIMIT ?", kind, kind, limit)
}
func (s *Store) History(ctx context.Context, entity string) ([]Record, error) {
	return readRows(ctx, s.db, "SELECT body FROM records WHERE entity=? ORDER BY seq DESC", entity)
}
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	var body string
	err := s.db.QueryRowContext(ctx, "SELECT body FROM records WHERE id=?", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return decodeRecord(body)
}
func (s *Store) Put(ctx context.Context, author string, in WriteInput) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Kind == "fact" {
		in.Entity = "fact/" + Hash(normalize(in.Subject)+"\x00"+normalize(in.Predicate))
	}
	if in.Entity == "" {
		in.Entity = "notes/" + NewID()
	}
	if searchable(in.Kind) || in.Kind == "prompt_template" {
		in.Title = Redact(in.Title)
		in.Text = Redact(in.Text)
		in.Value = Redact(in.Value)
		in.Subject = Redact(in.Subject)
		in.Predicate = Redact(in.Predicate)
		in.Source = Redact(in.Source)
		in.Tags = slices.Clone(in.Tags)
		for i := range in.Tags {
			in.Tags[i] = Redact(in.Tags[i])
		}
		in.Links = slices.Clone(in.Links)
		for i := range in.Links {
			in.Links[i].Target = Redact(in.Links[i].Target)
		}
		metadata := map[string]string{}
		for k, v := range in.Meta {
			metadata[k] = Redact(v)
		}
		if len(metadata) > 0 {
			in.Meta = metadata
		}
	}
	if in.Kind == "fact" {
		in.Entity = "fact/" + Hash(normalize(in.Subject)+"\x00"+normalize(in.Predicate))
	}
	heads, err := s.Heads(ctx, in.Entity)
	if err != nil {
		return Record{}, false, err
	}
	r := Record{ID: NewID(), Entity: in.Entity, Kind: in.Kind, Title: in.Title, Text: in.Text, Source: in.Source, Tags: in.Tags, Links: in.Links, Author: author, Created: Now(), Deleted: in.Deleted, Subject: in.Subject, Predicate: in.Predicate, Value: in.Value, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, Blob: in.Blob, Meta: in.Meta}
	// Idempotence never silently resolves an already divergent entity.
	if len(heads) == 1 && sameContent(heads[0], r) {
		return heads[0], false, nil
	}
	if in.Expected == nil {
		if len(heads) > 0 && in.Kind != "fact" {
			return Record{}, false, ErrConflict
		}
		r.Parents = []string{}
	} else {
		actual := make([]string, 0, len(heads))
		for _, h := range heads {
			actual = append(actual, h.ID)
		}
		sort.Strings(actual)
		expected := slices.Clone(in.Expected)
		sort.Strings(expected)
		if !slices.Equal(actual, expected) {
			return Record{}, false, ErrConflict
		}
		r.Parents = expected
	}
	if err = r.Validate(); err != nil {
		return r, false, err
	}
	_, err = s.mergeLocked(ctx, []Record{r})
	return r, err == nil, err
}
func sameContent(a, b Record) bool {
	a.ID = ""
	b.ID = ""
	a.Created = ""
	b.Created = ""
	a.Author = ""
	b.Author = ""
	a.Parents = nil
	b.Parents = nil
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Merge accepts immutable revisions in any delivery order. It rejects collisions,
// cycles and references across entities, including when the parent arrives later.
func (s *Store) Merge(ctx context.Context, records []Record) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mergeLocked(ctx, records)
}
func (s *Store) mergeLocked(ctx context.Context, records []Record) (int, error) {
	if len(records) > 10000 {
		return 0, errors.New("event batch too large")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	knowledgeChanged := false
	for _, r := range records {
		if err = r.Validate(); err != nil {
			return 0, err
		}
		body, e := json.Marshal(r)
		if e != nil {
			return 0, e
		}
		var existing string
		e = tx.QueryRowContext(ctx, "SELECT body FROM records WHERE id=?", r.ID).Scan(&existing)
		if e == nil {
			if existing != string(body) {
				return 0, errors.New("immutable revision id collision")
			}
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return 0, e
		}
		var bad int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE entity=? AND kind<>?", r.Entity, r.Kind).Scan(&bad); err != nil {
			return 0, err
		}
		if bad > 0 {
			return 0, errors.New("entity kind cannot change")
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM parents p JOIN records child ON child.id=p.child WHERE p.parent=? AND child.entity<>?", r.ID, r.Entity).Scan(&bad); err != nil {
			return 0, err
		}
		if bad > 0 {
			return 0, errors.New("parent belongs to a different entity")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO records(id,entity,kind,title,body,created,deleted) VALUES(?,?,?,?,?,?,?)", r.ID, r.Entity, r.Kind, r.Title, string(body), r.Created, r.Deleted); err != nil {
			return 0, err
		}
		for _, parent := range r.Parents {
			var entity string
			e = tx.QueryRowContext(ctx, "SELECT entity FROM records WHERE id=?", parent).Scan(&entity)
			if e == nil && entity != r.Entity {
				return 0, errors.New("cross-entity parent rejected")
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return 0, e
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO parents(child,parent) VALUES(?,?)", r.ID, parent); err != nil {
				return 0, err
			}
			if err = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (SELECT parent FROM parents WHERE child=? UNION SELECT p.parent FROM parents p JOIN ancestors a ON p.child=a.id) SELECT count(*) FROM ancestors WHERE id=?`, r.ID, r.ID).Scan(&bad); err != nil {
				return 0, err
			}
			if bad > 0 {
				return 0, errors.New("revision cycle rejected")
			}
		}
		if searchable(r.Kind) && r.Kind != "prompt" && r.Kind != "tool" && !r.Deleted {
			if _, err = tx.ExecContext(ctx, "INSERT INTO search(id,title,text,tags) VALUES(?,?,?,?)", r.ID, r.Title, r.Text+" "+r.Subject+" "+r.Predicate+" "+r.Value, strings.Join(r.Tags, " ")); err != nil {
				return 0, err
			}
		}
		count++
		knowledgeChanged = knowledgeChanged || (searchable(r.Kind) && r.Kind != "prompt" && r.Kind != "tool")
	}
	if knowledgeChanged {
		if _, err = tx.ExecContext(ctx, "DELETE FROM cache"); err != nil {
			return 0, err
		}
	}
	return count, tx.Commit()
}

type Event struct {
	Sequence int64  `json:"sequence"`
	Record   Record `json:"record"`
}

func (s *Store) Events(ctx context.Context, after int64, limit int) ([]Event, error) {
	if limit < 1 || limit > 10000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, "SELECT seq,body FROM records WHERE seq>? ORDER BY seq LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var body string
		if err = rows.Scan(&e.Sequence, &body); err != nil {
			return nil, err
		}
		if e.Record, err = decodeRecord(body); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) State(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM state WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func (s *Store) OnceState(ctx context.Context, key, value string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO state(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", key, value); err != nil {
		return "", err
	}
	var actual string
	if err = tx.QueryRowContext(ctx, "SELECT value FROM state WHERE key=?", key).Scan(&actual); err != nil {
		return "", err
	}
	return actual, tx.Commit()
}
func (s *Store) Count(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT r.kind,count(*) FROM records r WHERE r.deleted=0 AND "+headPredicate+" GROUP BY r.kind")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err = rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}
func (s *Store) Resolve(ctx context.Context, author, entity, chosen, reason string, expected []string) (Record, error) {
	if strings.TrimSpace(reason) == "" || len(expected) < 2 {
		return Record{}, errors.New("resolution requires a reason and all current alternatives")
	}
	r, err := s.Get(ctx, chosen)
	if err != nil {
		return r, err
	}
	if r.Entity != entity || !slices.Contains(expected, chosen) {
		return r, errors.New("chosen revision is not an alternative")
	}
	meta := map[string]string{}
	for k, v := range r.Meta {
		meta[k] = v
	}
	meta["resolution"] = Redact(reason)
	result, _, err := s.Put(ctx, author, WriteInput{Entity: entity, Kind: r.Kind, Title: r.Title, Text: r.Text, Source: r.Source, Tags: r.Tags, Links: r.Links, Expected: expected, Subject: r.Subject, Predicate: r.Predicate, Value: r.Value, ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, Blob: r.Blob, Meta: meta, Deleted: r.Deleted})
	return result, err
}
func (s *Store) String() string { return fmt.Sprintf("project %s", s.Project.ID) }
