package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

// planOnlyFlags is the help of the flags that only plan has.
const planOnlyFlags = "  --exit-code       exit with status 2 when the plan is not empty\n"

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
	"  0   the plan was made (with --exit-code: the database matches the\n" +
	"      declaration)\n" +
	"  1   the plan failed\n" +
	"  2   with --exit-code only: the plan is not empty\n"

// planOptions are the flags of plan.
type planOptions struct {
	common
	exitCode bool
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
		printError(e.stderr, interrupted(ctx, err, errInterrupted))
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
	fs.BoolVar(&o.exitCode, "exit-code", false, "")
	return fs
}

// makePlan reads the declaration and the settings, and only then connects to
// the database, so that a mistake in a file is reported without a database.
// The session is closed before makePlan returns, also when ctx is cancelled.
func makePlan(ctx context.Context, o planOptions, connect connectFunc) (p *plan.Plan, err error) {
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
	return plan.Build(ctx, decl, settings, db)
}
