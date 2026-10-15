//go:build !((darwin && (amd64 || arm64)) || (freebsd && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64 || s390x)) || (windows && (386 || amd64 || arm64)))

package opencode

import (
	"database/sql"

	"github.com/ncruces/go-sqlite3/driver"
)

func openSQLite(dsn string) (*sql.DB, error) {
	return driver.Open(dsn)
}
