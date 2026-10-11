// Package sqldb keeps the catalog SQL shared between embedded clients and the
// PostgreSQL hub. Project memory stores still use their own SQLite connection.
package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"strconv"
	"strings"
	"time"
)

type DB struct {
	*sql.DB
	Postgres bool
}
type Tx struct {
	*sql.Tx
	postgres bool
}

func Wrap(db *sql.DB) *DB { return &DB{DB: db} }
func OpenPostgres(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	cfg.ConnectTimeout = 10 * time.Second
	cfg.RuntimeParams["statement_timeout"] = "15000"
	cfg.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("PostgreSQL connection failed; check server and credentials")
	}
	return &DB{DB: db, Postgres: true}, nil
}

// Queries here are constant application SQL. Ignore quoted literals while
// translating positional placeholders; caller data is always a bound value.
func bind(pg bool, q string, args []any) (string, []any) {
	if !pg {
		return q, args
	}
	var b strings.Builder
	n := 0
	quote := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			if quote && i+1 < len(q) && q[i+1] == '\'' {
				b.WriteString("''")
				i++
				continue
			}
			quote = !quote
		}
		if c == '?' && !quote {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(c)
		}
	}
	values := make([]any, len(args))
	for i, v := range args {
		if x, ok := v.(bool); ok {
			if x {
				values[i] = int64(1)
			} else {
				values[i] = int64(0)
			}
		} else {
			values[i] = v
		}
	}
	return b.String(), values
}
func (d *DB) Exec(q string, a ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), q, a...)
}
func (d *DB) ExecContext(c context.Context, q string, a ...any) (sql.Result, error) {
	q, a = bind(d.Postgres, q, a)
	return d.DB.ExecContext(c, q, a...)
}
func (d *DB) Query(q string, a ...any) (*sql.Rows, error) {
	return d.QueryContext(context.Background(), q, a...)
}
func (d *DB) QueryContext(c context.Context, q string, a ...any) (*sql.Rows, error) {
	q, a = bind(d.Postgres, q, a)
	return d.DB.QueryContext(c, q, a...)
}
func (d *DB) QueryRow(q string, a ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), q, a...)
}
func (d *DB) QueryRowContext(c context.Context, q string, a ...any) *sql.Row {
	q, a = bind(d.Postgres, q, a)
	return d.DB.QueryRowContext(c, q, a...)
}
func (d *DB) Begin() (*Tx, error) { return d.BeginTx(context.Background(), nil) }
func (d *DB) BeginTx(c context.Context, o *sql.TxOptions) (*Tx, error) {
	t, e := d.DB.BeginTx(c, o)
	if e != nil {
		return nil, e
	}
	return &Tx{t, d.Postgres}, nil
}
func (t *Tx) Exec(q string, a ...any) (sql.Result, error) {
	return t.ExecContext(context.Background(), q, a...)
}
func (t *Tx) ExecContext(c context.Context, q string, a ...any) (sql.Result, error) {
	q, a = bind(t.postgres, q, a)
	return t.Tx.ExecContext(c, q, a...)
}
func (t *Tx) QueryRow(q string, a ...any) *sql.Row {
	return t.QueryRowContext(context.Background(), q, a...)
}
func (t *Tx) QueryRowContext(c context.Context, q string, a ...any) *sql.Row {
	q, a = bind(t.postgres, q, a)
	return t.Tx.QueryRowContext(c, q, a...)
}
