package opencode

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dbTimeout bounds every read of OpenCode's database, which another
// process may be writing to.
const dbTimeout = 5 * time.Second

// readOnlyDSN builds a SQLite URI that opens path without writing to it.
//
// With immutable set, SQLite reads the file as a snapshot: no locks and no
// -wal/-shm side files. Otherwise mode=ro is used, which reads consistently
// through the WAL of a database another process has open (and therefore
// shares its -shm). query_only guards against accidental writes and
// busy_timeout waits briefly for a writer instead of failing.
func readOnlyDSN(path string, immutable bool) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	q := url.Values{}
	if immutable {
		q.Set("immutable", "1")
	}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "query_only(1)")
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	return u.String(), nil
}

// withReadOnlyDB runs fn against the database at path, opened so that
// nothing is written to disk. fn may run twice and must reset any state it
// fills.
//
// OpenCode keeps its database in WAL mode. When no -wal file exists, no
// process has the database open and everything is in the main file, so it
// is read as immutable; opening it with mode=ro instead would leave fresh
// -wal and -shm files behind. If a -wal file appears during that read,
// OpenCode started meanwhile and the read is repeated with mode=ro, which
// is also what is used whenever the -wal file exists from the start.
func withReadOnlyDB(ctx context.Context, path string, fn func(context.Context, *sql.DB) error) error {
	wal := path + "-wal"
	if !pathExists(wal) {
		err := queryDB(ctx, path, true, fn)
		if !pathExists(wal) {
			return err
		}
	}
	return queryDB(ctx, path, false, fn)
}

func queryDB(ctx context.Context, path string, immutable bool, fn func(context.Context, *sql.DB) error) error {
	dsn, err := readOnlyDSN(path, immutable)
	if err != nil {
		return err
	}
	db, err := openSQLite(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	return fn(ctx, db)
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
