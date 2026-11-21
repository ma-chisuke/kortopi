package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// planOnlyFlags is the help of the flags that only plan has.
const planOnlyFlags = "  --format <name>   text (the default), or sql: print the plan as SQL\n" +
	"                    statements that run as they are\n" +
	"  --exit-code       exit with status 2 when the plan is not empty\n" +
	"  --no-pending      fail on a table or a column that the database lacks,\n" +
	"                    instead of showing it as waiting for the schema change\n"

// planFlags lists the flags of plan, for its help.
var planFlags = sourceFlags("plan") + planOnlyFlags + connectionFlags

// planUsage is the short help of plan, which follows an error in its flags.
var planUsage = "usage: kortopi plan [flags]\n" +
	"\n" +
	"flags:\n" +
	planFlags

// planHelp is the help of plan.
var planHelp = "usage: kortopi plan [flags]\n" +
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
	"  0   the plan was made (with --exit-code: there is nothing to apply)\n" +
	"  1   the plan failed\n" +
	"  2   with --exit-code only: the plan is not empty\n"

// planOptions are the flags of plan.
type planOptions struct {
	common
	// sql is true for --format sql.
	sql      bool
	exitCode bool
	// noPending is true for --no-pending (ADR-0034).
	noPending bool
}

// errInterrupted replaces the error of a plan that an interrupt stopped.
var errInterrupted = errors.New("interrupted: the plan was stopped, and the database is as it was")

// runPlan runs `kortopi plan`.
func runPlan(ctx context.Context, args []string, e env) int {
	o, err := parsePlan(args)
	if err != nil {
		return flagError(e, err, planHelp, planUsage)
	}
	var sql *bytes.Buffer
	if o.sql {
		sql = &bytes.Buffer{}
	}
	p, err := makePlan(ctx, o, e.connect, sql)
	if err != nil {
		printError(e.stderr, interrupted(ctx, err, errInterrupted))
		return exitError
	}
	if sql != nil {
		_, err = e.stdout.Write(sql.Bytes())
	} else {
		err = plan.Render(e.stdout, p)
	}
	if err != nil {
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
	var o planOptions
	if err := parse(planFlagSet(&o), args, "give the data directory with --dir"); err != nil {
		return planOptions{}, err
	}
	return o, nil
}

// planFlagSet returns the flags of plan, which parsing writes into o.
func planFlagSet(o *planOptions) *flag.FlagSet {
	fs := newFlagSet("plan")
	o.define(fs)
	fs.Func("format", "", func(s string) error {
		switch s {
		case "text":
			o.sql = false
		case "sql":
			o.sql = true
		default:
			return errors.New("give text or sql")
		}
		return nil
	})
	fs.BoolVar(&o.exitCode, "exit-code", false, "")
	fs.BoolVar(&o.noPending, "no-pending", false, "")
	return fs
}

// makePlan reads the declaration and the settings, and only then connects to
// the database, so that a mistake in a file is reported without a database.
// When sql is not nil, it writes the plan into it as SQL (ADR-0020), which
// needs the session. The session is closed before makePlan returns, also
// when ctx is cancelled.
func makePlan(ctx context.Context, o planOptions, connect connectFunc, sql *bytes.Buffer) (p *plan.Plan, err error) {
	decl, settings, err := load(o.common)
	if err != nil {
		return nil, err
	}
	db, err := connect(ctx, target{conn: o.conn, dir: decl.Dir})
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := closeSession(ctx, db); closeErr != nil && err == nil {
			p, err = nil, closeErr
		}
	}()
	p, err = plan.Build(ctx, decl, settings, db, plan.Options{NoPending: o.noPending})
	if err != nil || sql == nil {
		return p, err
	}
	h := script.Header{Dir: decl.Dir}
	if n, ok := db.(databaseNamer); ok {
		h.Database = n.DatabaseName()
	}
	if err := script.Write(ctx, sql, p, db, h); err != nil {
		return nil, fmt.Errorf("write the plan as SQL: %w", err)
	}
	return p, nil
}

// databaseNamer is a session that can say the name of its database, for the
// header of a plan as SQL. The name is all that the header says of the
// connection.
type databaseNamer interface {
	DatabaseName() string
}
