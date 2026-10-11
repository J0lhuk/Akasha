package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"akasha/internal/core"
	"akasha/internal/vault"
)

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}
type SyncReport struct {
	Pushed     int   `json:"pushed"`
	Pulled     int   `json:"pulled"`
	PullCursor int64 `json:"pull_cursor"`
	PushCursor int64 `json:"push_cursor"`
}

func Login(ctx context.Context, address, username, password string) (core.Session, error) {
	// URL/TLS/redirect validation is shared with normal sync; the placeholder is
	// not a credential and is removed before the unauthenticated login request.
	c, err := NewClient(address, strings.Repeat("0", 32))
	if err != nil {
		return core.Session{}, err
	}
	c.Token = ""
	var session core.Session
	err = c.Call(ctx, "POST", "/v1/login", map[string]string{"username": username, "password": password}, &session)
	return session, err
}

func NewClient(address, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(address, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("sync URL must be an origin without credentials, path or query")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := host == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("remote synchronization requires HTTPS")
	}
	if len(token) < 32 {
		return nil, errors.New("sync access token required")
	}
	return &Client{URL: u.String(), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("sync redirects are rejected") }}}, nil
}
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if len(b) > 32<<20 {
			return errors.New("sync request exceeds limit")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return errors.New("sync response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("sync request %s failed (HTTP %d)", path, response.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
func (c *Client) Catalog(ctx context.Context) ([]Enrollment, error) {
	var out []Enrollment
	err := c.Call(ctx, "GET", "/v1/catalog", nil, &out)
	return out, err
}
func (c *Client) Enroll(ctx context.Context, space core.Space, project core.Project) error {
	space.Root = ""
	project.Root = ""
	return c.Call(ctx, "POST", "/v1/enroll", Enrollment{space, project}, nil)
}
func cursor(ctx context.Context, s *core.Store, key string) (int64, error) {
	raw, err := s.State(ctx, key)
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid saved cursor")
	}
	return n, nil
}
func (c *Client) Sync(ctx context.Context, s *core.Store, key vault.Key) (SyncReport, error) {
	report := SyncReport{}
	// Pull first so a restored checkpoint learns the hub's canonical envelopes
	// before retrying its immutable revisions. This also discovers offline edits.
	initial, err := c.Pull(ctx, s, key)
	if err != nil {
		return report, err
	}
	report.Pulled = initial.Pulled
	report.PullCursor = initial.PullCursor
	scope := core.Hash(c.URL)
	pushKey := "sync/" + scope + "/push"
	pullKey := "sync/" + scope + "/pull"
	after, err := cursor(ctx, s, pushKey)
	if err != nil {
		return report, err
	}
	for {
		events, e := s.Events(ctx, after, 20)
		if e != nil {
			return report, e
		}
		if len(events) == 0 {
			break
		}
		wire := []Envelope{}
		size := 0
		last := after
		for _, event := range events {
			envelope, e := Encrypt(key, s.Project, event.Record)
			if e != nil {
				return report, e
			}
			envelope, e = saveWire(ctx, s, scope, envelope)
			if e != nil {
				return report, e
			}
			length := len(envelope.Ciphertext)*4/3 + 1024
			if size+length > 24<<20 && len(wire) > 0 {
				break
			}
			wire = append(wire, envelope)
			size += length
			last = event.Sequence
		}
		var ack struct {
			Added int `json:"added"`
		}
		if err = c.Call(ctx, "POST", "/v1/events/"+s.Project.ID, wire, &ack); err != nil {
			return report, err
		}
		if err = s.SetState(ctx, pushKey, strconv.FormatInt(last, 10)); err != nil {
			return report, err
		}
		report.Pushed += ack.Added
		after = last
	}
	report.PushCursor = after
	after, err = cursor(ctx, s, pullKey)
	if err != nil {
		return report, err
	}
	for {
		var page Page
		if err = c.Call(ctx, "GET", "/v1/events/"+s.Project.ID+"?after="+strconv.FormatInt(after, 10), nil, &page); err != nil {
			return report, err
		}
		if len(page.Events) == 0 {
			if after != page.Head {
				return report, errors.New("incomplete sync page")
			}
			break
		}
		records := make([]core.Record, 0, len(page.Events))
		last := after
		for _, event := range page.Events {
			if event.Sequence != last+1 {
				return report, errors.New("non-contiguous project sync log")
			}
			r, e := Decrypt(key, s.Project, event.Envelope)
			if e != nil {
				return report, e
			}
			records = append(records, r)
			last = event.Sequence
		}
		n, e := s.Merge(ctx, records)
		if e != nil {
			return report, e
		}
		for _, event := range page.Events {
			if _, err = saveWire(ctx, s, scope, event.Envelope); err != nil {
				return report, err
			}
		}
		// Advance only after the entire authenticated page is committed locally.
		if err = s.SetState(ctx, pullKey, strconv.FormatInt(last, 10)); err != nil {
			return report, err
		}
		after = last
		report.Pulled += n
		if after == page.Head {
			break
		}
	}
	report.PullCursor = after
	return report, nil
}

func (c *Client) Pull(ctx context.Context, s *core.Store, key vault.Key) (SyncReport, error) {
	report := SyncReport{}
	scope := core.Hash(c.URL)
	pullKey := "sync/" + scope + "/pull"
	after, err := cursor(ctx, s, pullKey)
	if err != nil {
		return report, err
	}
	for {
		var page Page
		if err = c.Call(ctx, "GET", "/v1/events/"+s.Project.ID+"?after="+strconv.FormatInt(after, 10), nil, &page); err != nil {
			return report, err
		}
		if len(page.Events) == 0 {
			if after != page.Head {
				return report, errors.New("incomplete sync page")
			}
			break
		}
		records := make([]core.Record, 0, len(page.Events))
		last := after
		for _, event := range page.Events {
			if event.Sequence != last+1 {
				return report, errors.New("non-contiguous project sync log")
			}
			r, e := Decrypt(key, s.Project, event.Envelope)
			if e != nil {
				return report, e
			}
			records = append(records, r)
			last = event.Sequence
		}
		n, e := s.Merge(ctx, records)
		if e != nil {
			return report, e
		}
		for _, event := range page.Events {
			data, e := json.Marshal(event.Envelope)
			if e != nil {
				return report, e
			}
			if err = s.SetState(ctx, "wire/"+scope+"/"+event.Envelope.ID, string(data)); err != nil {
				return report, err
			}
		}
		if err = s.SetState(ctx, pullKey, strconv.FormatInt(last, 10)); err != nil {
			return report, err
		}
		after = last
		report.Pulled += n
		if after == page.Head {
			break
		}
	}
	report.PullCursor = after
	return report, nil
}
