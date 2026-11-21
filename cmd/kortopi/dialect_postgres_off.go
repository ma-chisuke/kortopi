//go:build kortopi_no_postgres

package main

import "github.com/ma-chisuke/kortopi/internal/dialect"

// postgresDialects returns nothing: the build tag kortopi_no_postgres leaves
// PostgreSQL out of this build (ADR-0036).
func postgresDialects() []dialect.Dialect {
	return nil
}
