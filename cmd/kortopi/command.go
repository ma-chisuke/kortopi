package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"time"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// This file holds what plan and apply share: the flags of the declaration
// and of the connection, their help, the reading of the files, the session,
// and the handling of an interrupt.

// dirFlag is the help of --dir for plan and apply.
const dirFlag = "  --dir <path>      the data directory, which holds the declaration and\n" +
	"                    kortopi.toml (default: the current directory)\n"

// usageDirFlag is the help of --dir in the help of kortopi, which lists the
// flags of every command: the directory of an export may not exist yet.
const usageDirFlag = "  --dir <path>      the data directory of the declaration and kortopi.toml,\n" +
	"                    which an export creates if it is missing (default: the\n" +
	"                    current directory)\n"

// schemaFlag returns the help of --schema for plan and apply, whose name
// says what the command does with the declared tables of a schema.
func schemaFlag(command string) string {
	return "  --schema <name>   " + command + " only the tables of this schema; repeat the flag for\n" +
		"                    more schemas\n"
}

// sourceFlags returns the help of --dir and --schema for plan and apply.
func sourceFlags(command string) string {
	return dirFlag + schemaFlag(command)
}

// lockTimeoutFlag is the help of --lock-timeout, which plan, apply, and
// export share (ADR-0044).
const lockTimeoutFlag = "  --lock-timeout <duration>\n" +
	"                    stop when a statement waits longer than this for a lock\n" +
	"                    that another session holds, such as 10s, 500ms, or\n" +
	"                    1m30s (default: the lock_timeout of the session)\n"

// maxLockTimeout is the longest --lock-timeout.
const maxLockTimeout = 24 * time.Hour

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
// also after an interrupt. After an interrupt, the session waits less
// (dialect.Options.GiveUp).
const closeTimeout = 10 * time.Second

// common are the flags that plan, apply, and export share.
type common struct {
	dir     string
	schemas []string
	conn    connection
	// lockTimeout is --lock-timeout, or 0 without it.
	lockTimeout time.Duration
}

// connection holds the flags that override the connection settings of libpq.
// An empty field leaves the setting to libpq. They are the flags of the
// first dialect, PostgreSQL, and the command passes them to the dialect as
// they are (ADR-0036).
type connection struct {
	host   string
	port   string
	user   string
	dbname string
}

// open opens a session of d on the database that the flags of c name, for
// what opts say, with the lock timeout of the flags, and the second
// interrupt of e.
func (c common) open(ctx context.Context, d dialect.Dialect, opts dialect.Options, e env) (dialect.Session, error) {
	opts.Host, opts.Port, opts.User, opts.Database = c.conn.host, c.conn.port, c.conn.user, c.conn.dbname
	opts.LockTimeout, opts.GiveUp = c.lockTimeout, e.again
	return d.Open(ctx, opts)
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
	fs.Func("lock-timeout", "", func(s string) error {
		d, err := time.ParseDuration(s)
		if err != nil || d < time.Millisecond || d > maxLockTimeout {
			return errors.New("give a duration from 1ms to 24h, such as 10s, 500ms, or 1m30s")
		}
		c.lockTimeout = d
		return nil
	})
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

// load reads the settings and the declaration of the data directory, with
// the parser of SQL declarations of the dialect, checks the settings against
// the whole declaration, and narrows the declaration to the schemas of
// --schema. It needs no database, so that a mistake in a file is reported
// before kortopi connects.
func load(c common, parser declaration.SQLParser) (*declaration.Declaration, *config.Settings, error) {
	// The directory comes first, so that a path to a file is named as such,
	// and not as a file kortopi.toml under it.
	if err := declaration.CheckDir(c.dir, false); err != nil {
		return nil, nil, err
	}
	settings, err := config.Load(c.dir)
	if err != nil {
		return nil, nil, err
	}
	decl, err := declaration.Load(c.dir, parser)
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

// closeSession closes s with a context of its own, so that the transaction is
// rolled back and the connection closed also after an interrupt, and bounded
// in time.
func closeSession(ctx context.Context, s dialect.Session) error {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	return s.Close(closeCtx)
}

// interrupted returns instead when ctx was cancelled by an interrupt, and
// err otherwise: after an interrupt, the error of the database only says that
// a statement was cancelled. When the database did not confirm that the
// session ended, the error that says so follows instead (ADR-0043).
func interrupted(ctx context.Context, err, instead error) error {
	if ctx.Err() == nil {
		return err
	}
	var notEnded *dialect.NotEndedError
	if errors.As(err, &notEnded) {
		return errors.Join(instead, notEnded)
	}
	return instead
}

// withNotEnded returns err, and the error of closeErr that says that the
// database did not confirm the end of the session after an interrupt, if it
// holds one: the other errors of a close after a failure add nothing.
func withNotEnded(err, closeErr error) error {
	var notEnded *dialect.NotEndedError
	if errors.As(closeErr, &notEnded) {
		return errors.Join(err, notEnded)
	}
	return err
}
