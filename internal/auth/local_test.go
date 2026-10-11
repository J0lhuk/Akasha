package auth

import (
	"testing"
)

func TestLocalTokenSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	a, err := LocalToken(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LocalToken(root)
	if err != nil || a != b || len(a) != 64 {
		t.Fatal("local authentication token not stable", err)
	}
}
