//go:build (darwin && (amd64 || arm64)) || (freebsd && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64 || s390x)) || (windows && (386 || amd64 || arm64))

package opencode

import (
	"database/sql"

	_ "modernc.org/sqlite" // The driver internal/db uses on this platform.
)

func openSQLite(dsn string) (*sql.DB, error) {
	return sql.Open("sqlite", dsn)
}
