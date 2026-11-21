//go:build !kortopi_no_postgres

package main

import (
	"github.com/ma-chisuke/kortopi/internal/dialect"
	"github.com/ma-chisuke/kortopi/internal/postgres"
)

// postgresDialects returns PostgreSQL, which every build holds unless it sets
// the build tag kortopi_no_postgres (ADR-0036). This file is the one file of
// the command that names it; dialect_postgres_off.go takes its place in a
// build with the tag.
func postgresDialects() []dialect.Dialect {
	return []dialect.Dialect{postgres.Dialect{}}
}
