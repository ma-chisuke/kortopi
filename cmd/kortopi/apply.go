package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/dialect"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// applyOnlyFlags is the help of the flags that only apply has (ADR-0018).
var applyOnlyFlags = "  --allow-delete    delete the rows that the declaration lacks, in tables\n" +
	"                    of mode sync\n" +
	"  --max-delete <n>  with --allow-delete: change nothing when the deletions\n" +
	"                    would delete or change more than n rows, counting the\n" +
	"                    rows that foreign keys delete or change with them, at\n" +
	"                    every depth (default: " + strconv.Itoa(apply.DefaultMaxDelete) + ")\n"

// applyFlags lists the flags of apply, for its help.
var applyFlags = sourceFlags("apply") + applyOnlyFlags + connectionFlags

// applyUsage is the short help of apply, which follows an error in its
// flags.
var applyUsage = "usage: kortopi apply [flags]\n" +
	"\n" +
	"flags:\n" +
	applyFlags

// applyHelp is the help of apply.
var applyHelp = "usage: kortopi apply [flags]\n" +
	"\n" +
	"Change the rows of the database to match the declaration: insert, update,\n" +
	"and delete the rows that a plan shows, in one transaction. The plan is\n" +
	"printed before the first change, and what was written after the commit.\n" +
	"When any check or statement fails, nothing is changed. Rows are deleted\n" +
	"only in tables of mode sync, and only with --allow-delete.\n" +
	"\n" +
	"flags:\n" +
	applyFlags +
	"\n" +
	connectionHelp +
	"\n" +
	"exit status:\n" +
	"  0   the apply committed its changes, or there was nothing to change\n" +
	"  1   the apply failed: nothing was changed, unless the error says that\n" +
	"      the outcome of the commit is not known\n"

// applyOptions are the flags of apply.
type applyOptions struct {
	common
	allowDelete bool
	maxDelete   int
	// maxDeleteSet is true when the user gave --max-delete.
	maxDeleteSet bool
}

var (
	// errApplyInterrupted replaces the error of an apply that an interrupt
	// stopped.
	errApplyInterrupted = errors.New("interrupted: the apply was stopped")
	// errNothingChanged is the last line of a failed apply.
	errNothingChanged = errors.New("nothing was changed")
)

// runApply runs `kortopi apply`.
func runApply(ctx context.Context, args []string, e env) int {
	o, err := parseApply(args)
	if err != nil {
		return flagError(e, err, applyHelp, applyUsage)
	}
	r, closeErr, err := makeApply(ctx, o, e)
	switch {
	case errors.Is(err, apply.ErrUnknownOutcome):
		// The changes may have been made or not, also after an interrupt:
		// the error says how to find out, and nothing claims otherwise.
		printError(e.stderr, err)
		return exitError
	case err != nil:
		printError(e.stderr, interrupted(ctx, err, errApplyInterrupted))
		printError(e.stderr, errNothingChanged)
		return exitError
	}
	if err := apply.RenderSummary(e.stdout, r); err != nil {
		if !committed(r) {
			printError(e.stderr, fmt.Errorf("write the result of the apply: %w", err))
			printError(e.stderr, errNothingChanged)
			return exitError
		}
		// The changes are lasting: the status says so, and the error says
		// what was lost.
		printError(e.stderr, fmt.Errorf("the apply committed its changes, but its result could not be written: %w", err))
	}
	if closeErr != nil {
		printError(e.stderr, fmt.Errorf("the apply committed its changes, but closing the connection failed: %w", closeErr))
	}
	return exitOK
}

// parseApply parses the flags of apply.
func parseApply(args []string) (applyOptions, error) {
	var o applyOptions
	if err := parse(applyFlagSet(&o), args, "give the data directory with --dir"); err != nil {
		return applyOptions{}, err
	}
	if o.maxDeleteSet && !o.allowDelete {
		return applyOptions{}, errors.New("flag --max-delete needs --allow-delete, without which an apply deletes no row: add --allow-delete, or remove --max-delete")
	}
	return o, nil
}

// applyFlagSet returns the flags of apply, which parsing writes into o.
func applyFlagSet(o *applyOptions) *flag.FlagSet {
	fs := newFlagSet("apply")
	o.define(fs)
	o.maxDelete = apply.DefaultMaxDelete
	fs.BoolVar(&o.allowDelete, "allow-delete", false, "")
	fs.Func("max-delete", "", func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return errors.New("give the largest number of rows to delete, a whole number from 0")
		}
		o.maxDelete, o.maxDeleteSet = n, true
		return nil
	})
	return fs
}

// makeApply chooses the dialect, reads the declaration and the settings, and
// only then connects to the database and applies. It prints the plan on
// standard output as soon as the apply has it, before the guards and the
// first write, so that the output shows what the apply was about to do also
// when it fails. It returns the error of closing the session apart when the
// apply committed: the changes are lasting all the same.
func makeApply(ctx context.Context, o applyOptions, e env) (r *apply.Result, closeErr, err error) {
	d, err := e.dialects.Choose()
	if err != nil {
		return nil, nil, err
	}
	decl, settings, err := load(o.common, d.Parser())
	if err != nil {
		return nil, nil, err
	}
	db, err := o.conn.open(ctx, d, dialect.Options{Dir: decl.Dir, Apply: true, Wait: e.stderr})
	if err != nil {
		return nil, nil, err
	}
	r, err = apply.Run(ctx, decl, settings, db, apply.Options{
		AllowDelete: o.allowDelete,
		MaxDelete:   o.maxDelete,
		Planned: func(p *plan.Plan) error {
			if err := plan.Render(e.stdout, p); err != nil {
				return fmt.Errorf("write the plan: %w", err)
			}
			return nil
		},
	})
	closeErr = closeSession(ctx, db)
	switch {
	case err != nil:
		return nil, nil, err
	case closeErr != nil && !committed(r):
		return nil, nil, closeErr
	}
	return r, closeErr, nil
}

// committed reports whether an apply committed: it does when it wrote a row
// (apply.Run).
func committed(r *apply.Result) bool {
	return r.Inserted+r.Updated+r.Deleted > 0
}
