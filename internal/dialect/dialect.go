// Package dialect is the one place where a command of kortopi meets a
// database (ADR-0019, ADR-0036).
//
// A dialect is the package of one database, such as internal/postgres: it
// reads the SQL declarations in the grammar of its database, and opens the
// sessions that plan, apply, and export against it. This package defines
// what a command needs from a dialect, and a registry of the dialects that a
// build holds. It knows no database itself, and imports no package of a
// dialect: the linter enforces that (.golangci.yml).
package dialect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// Dialect is one database that kortopi supports.
type Dialect interface {
	// Name names the dialect in the registry, such as postgres.
	Name() string

	// Parser returns the reader of the SQL declarations, in the grammar of
	// the database.
	Parser() declaration.SQLParser

	// Open connects to the database that opts name, and starts the
	// transaction of a session for what opts say.
	Open(ctx context.Context, opts Options) (Session, error)
}

// Options name the database and say what a session is for.
//
// Host, Port, User, and Database are the flags of the connection, as the
// command collected them. An empty field leaves the setting to the dialect,
// which may read it from the environment. They are the flags of the first
// dialect, PostgreSQL; how they generalize is decided with a second dialect
// (ADR-0036).
type Options struct {
	Host     string
	Port     string
	User     string
	Database string
	// Dir is the data directory of the declaration, from which the session
	// reads the declared files.
	Dir string
	// Apply opens a session that can write, for an apply. Without Apply or
	// Export, the session is for a plan, and writes nothing.
	Apply bool
	// Export opens a session that only reads, for an export. Apply and Export
	// exclude each other.
	Export bool
	// Wait gets one line when an apply waits for another apply to finish. It
	// may be nil.
	Wait io.Writer
}

// Session is a session on the target database, for any command: each
// command uses the methods that it needs. Its transaction is rolled back
// when it closes, unless an apply committed it.
type Session interface {
	apply.Database
	script.Database
	export.Database

	// DatabaseName returns the name of the database of the session, and
	// nothing else of the connection, for the header of a plan as SQL.
	DatabaseName() string

	// Close rolls back the transaction of the session, unless it was
	// committed, and closes it.
	Close(ctx context.Context) error
}

// ErrNoDialect is the error of Choose when the build holds no dialect: its
// build tags left every database out (ADR-0036).
var ErrNoDialect = errors.New("this build of kortopi supports no database: " +
	"build kortopi again without the build tags that leave a database out, kortopi_no_<database>")

// Registry holds the dialects of a build. Each dialect registers itself
// when the program starts, in a file of the command that a build tag can
// leave out. The zero value is an empty registry.
type Registry struct {
	dialects []Dialect
}

// Register adds d to r. A registration file of the command calls it in the
// initializer of a blank variable, which runs when the program starts:
// the linter of this repository allows no init function. The result has no
// use but that. Register panics when r holds a dialect of the same name,
// which is a mistake of the build.
func (r *Registry) Register(d Dialect) struct{} {
	for _, other := range r.dialects {
		if other.Name() == d.Name() {
			panic(fmt.Sprintf("dialect %s is registered twice", d.Name()))
		}
	}
	r.dialects = append(r.dialects, d)
	return struct{}{}
}

// Choose returns the dialect that a command works with. A build that holds
// one dialect uses it, with no flag and no setting.
//
// How a user chooses among several dialects of one build is not decided
// (ADR-0036): this is where that choice will be made. Until then, a build
// with several dialects is an error.
func (r *Registry) Choose() (Dialect, error) {
	switch len(r.dialects) {
	case 0:
		return nil, ErrNoDialect
	case 1:
		return r.dialects[0], nil
	}
	names := make([]string, len(r.dialects))
	for i, d := range r.dialects {
		names[i] = d.Name()
	}
	return nil, fmt.Errorf("this build of kortopi supports several databases, %s, and cannot choose among them yet: "+
		"build kortopi with one of them", strings.Join(names, ", "))
}
