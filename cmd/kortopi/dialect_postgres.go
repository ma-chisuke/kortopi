//go:build !kortopi_no_postgres

package main

import "github.com/ma-chisuke/kortopi/internal/postgres"

// PostgreSQL is in every build that does not set the build tag
// kortopi_no_postgres (ADR-0036). This file is the one file of the command
// that names it.
var _ = dialects.Register(postgres.Dialect{})
