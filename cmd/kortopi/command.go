package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/postgres"
)

// This file holds what plan and apply share: the flags of the declaration
// and of the connection, their help, the reading of the files, the session,
// and the handling of an interrupt.

// sourceFlags returns the help of --dir and --schema for a command, whose
// name says what it does with the tables of a schema.
func sourceFlags(command string) string {
	return "  --dir <path>      the data directory, which holds the declaration and\n" +
		"                    kortopi.toml (default: the current directory)\n" +
		"  --schema <name>   " + command + " only the tables of this schema; repeat the flag for\n" +
		"                    more schemas\n"
}

// connectionFlags is the help of the flags of the connection.
const connectionFlags = "  --host <host>     the host of the database server\n" +
	"  --port <port>     the port of the database server\n" +
	"  --user <user>     the user to connect as\n" +
	"  --dbname <name>   the database to connect to\n"

// connectionHelp says where the connection settings come from (ADR-0021).
const connectionHelp = "The connection comes from the environment variables of libpq, such as\n" +
	"PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,\n" +
	"and from the password file. The flags --host, --port, --user, and --dbname\n" +
	"override them. There is no flag for the password.\n"

// closeTimeout bounds the rollback and the close of the session, which run
// also after an interrupt.
const closeTimeout = 10 * time.Second

// common are the flags that plan and apply share.
type common struct {
	dir     string
	schemas []string
	conn    connection
}

// connection holds the flags that override the connection settings of libpq.
// An empty field leaves the setting to libpq.
type connection struct {
	host   string
	port   string
	user   string
	dbname string
}

// define defines the shared flags on fs, which parsing writes into c. It
// sets the default of --dir.
func (c *common) define(fs *flag.FlagSet) {
	c.dir = "."
	fs.Func("dir", "", func(s string) error {
		if s == "" {
			return errors.New("give the data directory")
		}
		c.dir = s
		return nil
	})
	fs.Func("schema", "", func(s string) error {
		if s == "" {
			return errors.New("give the name of a schema")
		}
		c.schemas = append(c.schemas, s)
		return nil
	})
	fs.Func("host", "", nameOnly(&c.conn.host, "the name or address of the host"))
	fs.Func("port", "", func(s string) error {
		if n, err := strconv.ParseUint(s, 10, 16); err != nil || n == 0 {
			return errors.New("give a port number from 1 to 65535")
		}
		c.conn.port = s
		return nil
	})
	fs.Func("user", "", nameOnly(&c.conn.user, "the name of the user"))
	fs.Func("dbname", "", nameOnly(&c.conn.dbname, "the name of the database"))
}

// nameOnly returns the function of a flag of the connection that takes a
// name, what, and sets it in dst. A connection string is refused, because it
// may hold a password, which belongs in the environment, and because libpq
// would read it as a name: the error does not repeat it.
func nameOnly(dst *string, what string) func(string) error {
	return func(s string) error {
		if connectionString(s) {
			return fmt.Errorf("it takes only %s, not a connection string: "+
				"give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD", what)
		}
		*dst = s
		return nil
	}
}

// load reads the settings and the declaration of the data directory, checks
// the settings against the whole declaration, and narrows the declaration to
// the schemas of --schema. It needs no database, so that a mistake in a file
// is reported before kortopi connects.
func load(c common) (*declaration.Declaration, *config.Settings, error) {
	settings, err := config.Load(c.dir)
	if err != nil {
		return nil, nil, err
	}
	decl, err := declaration.Load(c.dir, postgres.Parser{})
	if err != nil {
		return nil, nil, err
	}
	// Settings of a table in a schema that --schema leaves out are not a
	// mistake.
	if err := settings.CheckDeclared(decl.Names()); err != nil {
		return nil, nil, err
	}
	decl, err = decl.OnlySchemas(c.schemas)
	if err != nil {
		return nil, nil, err
	}
	return decl, settings, nil
}

// target says which database a command opens a session on, and what for.
type target struct {
	conn connection
	// dir is the data directory, from which the session reads the declared
	// files.
	dir string
	// apply opens a session that can write, for an apply. Without it, the
	// session is for a plan.
	apply bool
	// wait gets one line when an apply waits for another apply.
	wait io.Writer
}

// connectFunc opens a session on the target database. Tests replace it.
type connectFunc func(ctx context.Context, t target) (session, error)

// session is a session on the target database. Its transaction is rolled back
// when it closes, unless an apply committed it.
type session interface {
	apply.Database
	// Close rolls back the transaction of the session, unless it was
	// committed, and closes it.
	Close(ctx context.Context) error
}

// closeSession closes s with a context of its own, so that the transaction is
// rolled back and the connection closed also after an interrupt, and bounded
// in time.
func closeSession(ctx context.Context, s session) error {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	return s.Close(closeCtx)
}

// interrupted returns instead when ctx was cancelled by an interrupt, and
// err otherwise: after an interrupt, the error of the database only says that
// a statement was cancelled.
func interrupted(ctx context.Context, err, instead error) error {
	if ctx.Err() != nil {
		return instead
	}
	return err
}

// connectPostgres opens a session on a PostgreSQL server. It is the one part
// of plan and apply that needs a real database, so the tests replace it.
func connectPostgres(ctx context.Context, t target) (session, error) {
	s, err := postgres.Connect(ctx, postgres.Options{
		Host:     t.conn.host,
		Port:     t.conn.port,
		User:     t.conn.user,
		Database: t.conn.dbname,
		Dir:      t.dir,
		Apply:    t.apply,
		Wait:     t.wait,
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}
