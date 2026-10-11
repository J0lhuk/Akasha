package core

import (
	"context"
	"errors"
	"testing"
)

func TestAccountsLoginRevocationAndACL(t *testing.T) {
	m, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	ctx := context.Background()
	token := NewID() + NewID()
	if e = m.Bootstrap(token); e != nil {
		t.Fatal(e)
	}
	owner, e := m.Authenticate(token)
	if e != nil {
		t.Fatal(e)
	}
	user, e := m.CreateAccount(ctx, owner, "A", "coder", "invalid-padding-password")
	if e != nil {
		t.Fatal(e)
	}
	for _, password := range []string{"", "wrong-password-123"} {
		if _, e = m.Login(ctx, "coder", password, t.Name()); !errors.Is(e, ErrDenied) {
			t.Fatal("invalid password accepted", e)
		}
	}
	session, e := m.Login(ctx, "CODER", "invalid-padding-password", t.Name())
	if e != nil {
		t.Fatal(e)
	}
	if p, e := m.Authenticate(session.Token); e != nil || p.ID != user.ID || p.Admin {
		t.Fatal("session", p, e)
	}
	if _, e = m.CreateAccount(ctx, user, "illegal", "illegal", "generated-fixture-pass"); !errors.Is(e, ErrDenied) {
		t.Fatal("privilege escalation", e)
	}
	if e = m.SetAccount(ctx, owner, user.ID, "coder", "new-fixture-password"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Authenticate(session.Token); !errors.Is(e, ErrDenied) {
		t.Fatal("reset did not revoke", e)
	}
	session, e = m.Login(ctx, "coder", "new-fixture-password", t.Name())
	if e != nil {
		t.Fatal(e)
	}
	if e = m.RevokeSessions(ctx, owner, user.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Authenticate(session.Token); !errors.Is(e, ErrDenied) {
		t.Fatal("disabled session", e)
	}
	if _, e = m.Login(ctx, "coder", "new-fixture-password", t.Name()); !errors.Is(e, ErrDenied) {
		t.Fatal("disabled login", e)
	}
	if _, e = m.db.Exec("SELECT password_hash FROM accounts"); e != nil {
		t.Fatal(e)
	}
}
