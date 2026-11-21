package postgres

import (
	"context"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// Dialect is PostgreSQL as a dialect of kortopi, which the command
// registers (ADR-0036). The zero value is ready to use.
type Dialect struct{}

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
func (Dialect) Open(ctx context.Context, opts dialect.Options) (dialect.Session, error) {
	s, err := Connect(ctx, Options{
		Host:     opts.Host,
		Port:     opts.Port,
		User:     opts.User,
		Database: opts.Database,
		Dir:      opts.Dir,
		Apply:    opts.Apply,
		Export:   opts.Export,
		Wait:     opts.Wait,
	})
	if err != nil {
		// A nil *Session in the interface would not be nil.
		return nil, err
	}
	return s, nil
}
