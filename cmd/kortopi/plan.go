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
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/postgres"
)

// planFlags lists the flags of plan, for its help and for the help of
// kortopi.
const planFlags = "  --dir <path>      the data directory, which holds the declaration and\n" +
	"                    kortopi.toml (default: the current directory)\n" +
	"  --schema <name>   plan only the tables of this schema; repeat the flag for\n" +
	"                    more schemas\n" +
	"  --exit-code       exit with status 2 when the plan is not empty\n" +
	"  --host <host>     the host of the database server\n" +
	"  --port <port>     the port of the database server\n" +
	"  --user <user>     the user to connect as\n" +
	"  --dbname <name>   the database to connect to\n"

// connectionHelp says where the connection settings come from (ADR-0021).
const connectionHelp = "The connection comes from the environment variables of libpq, such as\n" +
	"PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,\n" +
	"and from the password file. The flags --host, --port, --user, and --dbname\n" +
	"override them. There is no flag for the password.\n"

// planUsage is the short help of plan, which follows an error in its flags.
const planUsage = "usage: kortopi plan [flags]\n" +
	"\n" +
	"flags:\n" +
	planFlags

// planHelp is the help of plan.
const planHelp = "usage: kortopi plan [flags]\n" +
	"\n" +
	"Show how the database differs from the declaration: the rows that each\n" +
	"table would gain, change, and lose. A plan changes no table of the\n" +
	"database.\n" +
	"\n" +
	"flags:\n" +
	planFlags +
	"\n" +
	connectionHelp +
	"\n" +
	"exit status:\n" +
	"  0   the plan was made (with --exit-code: the database matches the\n" +
	"      declaration)\n" +
	"  1   the plan failed\n" +
	"  2   with --exit-code only: the plan is not empty\n"

// closeTimeout bounds the rollback and the close of the session, which run
// also after an interrupt.
const closeTimeout = 10 * time.Second

// planOptions are the flags of plan.
type planOptions struct {
	dir      string
	schemas  []string
	exitCode bool
	conn     connection
}

// connection holds the flags that override the connection settings of libpq.
// An empty field leaves the setting to libpq.
type connection struct {
	host   string
	port   string
	user   string
	dbname string
}

// connectFunc opens a session on the target database. dir is the data
// directory, from which the session reads the declared files.
type connectFunc func(ctx context.Context, c connection, dir string) (session, error)

// session is a session on the target database that a plan reads.
type session interface {
	plan.Database
	// Close rolls back the transaction of the session and closes it.
	Close(ctx context.Context) error
}

// errInterrupted replaces the error of a plan that an interrupt stopped.
var errInterrupted = errors.New("interrupted: the plan was stopped, and the database is as it was")

// runPlan runs `kortopi plan`.
func runPlan(ctx context.Context, args []string, e env) int {
	o, err := parsePlan(args)
	if err != nil {
		return flagError(e, err, planHelp, planUsage)
	}
	p, err := makePlan(ctx, o, e.connect)
	if err != nil {
		if ctx.Err() != nil {
			err = errInterrupted
		}
		printError(e.stderr, err)
		return exitError
	}
	if err := plan.Render(e.stdout, p); err != nil {
		printError(e.stderr, fmt.Errorf("write the plan: %w", err))
		return exitError
	}
	if o.exitCode && !p.Empty() {
		return exitChanges
	}
	return exitOK
}

// parsePlan parses the flags of plan.
func parsePlan(args []string) (planOptions, error) {
	o := planOptions{dir: "."}
	if err := parse(planFlagSet(&o), args, "give the data directory with --dir"); err != nil {
		return planOptions{}, err
	}
	return o, nil
}

// planFlagSet returns the flags of plan, which parsing writes into o.
func planFlagSet(o *planOptions) *flag.FlagSet {
	fs := newFlagSet("plan")
	fs.Func("dir", "", func(s string) error {
		if s == "" {
			return errors.New("give the data directory")
		}
		o.dir = s
		return nil
	})
	fs.Func("schema", "", func(s string) error {
		if s == "" {
			return errors.New("give the name of a schema")
		}
		o.schemas = append(o.schemas, s)
		return nil
	})
	fs.BoolVar(&o.exitCode, "exit-code", false, "")
	fs.Func("host", "", nameOnly(&o.conn.host, "the name or address of the host"))
	fs.Func("port", "", func(s string) error {
		if n, err := strconv.ParseUint(s, 10, 16); err != nil || n == 0 {
			return errors.New("give a port number from 1 to 65535")
		}
		o.conn.port = s
		return nil
	})
	fs.Func("user", "", nameOnly(&o.conn.user, "the name of the user"))
	fs.Func("dbname", "", nameOnly(&o.conn.dbname, "the name of the database"))
	return fs
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

// makePlan reads the declaration and the settings, and only then connects to
// the database, so that a mistake in a file is reported without a database.
// The session is closed before makePlan returns, also when ctx is cancelled.
func makePlan(ctx context.Context, o planOptions, connect connectFunc) (p *plan.Plan, err error) {
	settings, err := config.Load(o.dir)
	if err != nil {
		return nil, err
	}
	decl, err := declaration.Load(o.dir, postgres.Parser{})
	if err != nil {
		return nil, err
	}
	// The settings are checked against the whole declaration: settings of a
	// table in a schema that --schema leaves out are not a mistake.
	if err := settings.CheckDeclared(decl.Names()); err != nil {
		return nil, err
	}
	decl, err = decl.OnlySchemas(o.schemas)
	if err != nil {
		return nil, err
	}

	db, err := connect(ctx, o.conn, decl.Dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		if closeErr := db.Close(closeCtx); closeErr != nil && err == nil {
			p, err = nil, closeErr
		}
	}()
	return plan.Build(ctx, decl, settings, db)
}

// connectPostgres opens a session on a PostgreSQL server. It is the one part
// of plan that needs a real database, so the tests replace it.
func connectPostgres(ctx context.Context, c connection, dir string) (session, error) {
	s, err := postgres.Connect(ctx, postgres.Options{
		Host:     c.host,
		Port:     c.port,
		User:     c.user,
		Database: c.dbname,
		Dir:      dir,
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}
