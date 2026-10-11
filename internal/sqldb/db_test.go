package sqldb

import "testing"

func TestPlaceholdersAndFlags(t *testing.T) {
	q, a := bind(true, "SELECT '?' AS literal WHERE enabled=? AND x=? AND 'it''s ?'='it''s ?'", []any{true, "data?"})
	if q != "SELECT '?' AS literal WHERE enabled=$1 AND x=$2 AND 'it''s ?'='it''s ?'" || a[0] != int64(1) || a[1] != "data?" {
		t.Fatal(q, a)
	}
}
