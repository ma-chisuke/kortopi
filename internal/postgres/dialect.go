package postgres

import (
	"context"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// Dialect is PostgreSQL as a dialect of kortopi, which the command
// registers (ADR-0036). The zero value is ready to use.
type Dialect struct {
	// settings are settings of the connection as a connection string of
	// keywords and values, such as password and sslmode, which a session
	// takes as it takes the environment variables of libpq, and in their
	// place: they override the environment, and the flags of the connection
	// override them. The command leaves them empty. Only the integration
	// tests set them, through WithSettings, so that a test of the command
	// leaves the environment of its process, which the tests that run in
	// parallel share, alone (ADR-0052).
	settings string
}

var (
	_ dialect.Dialect = Dialect{}
	_ dialect.Session = (*Session)(nil)
)

// Name returns postgres.
func (Dialect) Name() string {
	return "postgres"
}

// Parser returns the reader of SQL declarations, the parser of PostgreSQL
// itself (ADR-0015).
func (Dialect) Parser() declaration.SQLParser {
	return Parser{}
}

// Open connects to the database that opts name, as Connect does: an empty
// field of the connection falls back to the environment variables of libpq.
func (d Dialect) Open(ctx context.Context, opts dialect.Options) (dialect.Session, error) {
	o := options(opts)
	o.settings = d.settings
	s, err := Connect(ctx, o)
	if err != nil {
		// A nil *Session in the interface would not be nil.
		return nil, err
	}
	return s, nil
}

// options returns the Options of a session for the options of the command,
// every field as it is.
func options(o dialect.Options) Options {
	return Options{
		Host:        o.Host,
		Port:        o.Port,
		User:        o.User,
		Database:    o.Database,
		Dir:         o.Dir,
		Apply:       o.Apply,
		Export:      o.Export,
		Wait:        o.Wait,
		LockTimeout: o.LockTimeout,
		GiveUp:      o.GiveUp,
	}
}
