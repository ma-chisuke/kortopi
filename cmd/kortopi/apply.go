package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

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
var applyFlags = sourceFlags("apply") + applyOnlyFlags + lockTimeoutFlag + connectionFlags

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
	"only in tables of mode sync, and only with --allow-delete. Last before the\n" +
	"commit, the sequence of a column that the apply writes values into moves\n" +
	"beyond them; a commit that fails then leaves it moved.\n" +
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
	// errCommitStarted is the line of an interrupt that comes after the
	// commit has started, which it does not stop (ADR-0043).
	errCommitStarted = errors.New("the commit has started, so the interrupt does not stop it: " +
		"waiting for the answer of the database, so that the outcome is known: interrupt again to give up the wait")
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
		// An interrupt does not stop a commit that has started, so the error
		// of the commit is what happened, also after an interrupt.
		var commitErr *commitFailure
		if errors.As(err, &commitErr) {
			printError(e.stderr, err)
		} else {
			printError(e.stderr, interrupted(ctx, err, errApplyInterrupted))
		}
		printError(e.stderr, nothingChanged(err))
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

// nothingChanged returns the last line of a failed apply: errNothingChanged,
// or, when the apply had moved sequences before it failed, a line that
// names them: a rollback does not move a sequence back, so they stay where
// the apply moved them, and only skip values (ADR-0045).
func nothingChanged(err error) error {
	var moved *apply.SequencesMovedError
	if !errors.As(err, &moved) || len(moved.Moved) == 0 {
		return errNothingChanged
	}
	names := make([]string, len(moved.Moved))
	for i, m := range moved.Moved {
		names[i] = plan.FormatSequence(m.Name) + " to next value " + m.Goal
	}
	return fmt.Errorf("%w but the sequences that the apply had moved before its commit, which a rollback does not move back, so they only skip values: %s",
		errNothingChanged, strings.Join(names, ", "))
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
	db, err := o.open(ctx, d, dialect.Options{Dir: decl.Dir, Apply: true, Wait: e.stderr}, e)
	if err != nil {
		return nil, nil, err
	}
	r, err = apply.Run(ctx, decl, settings, committing{Session: db, run: ctx, stderr: e.stderr}, apply.Options{
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
		return nil, nil, withNotEnded(err, closeErr)
	case closeErr != nil && !committed(r):
		return nil, nil, closeErr
	}
	return r, closeErr, nil
}

// committing is the session of an apply, whose commit says on standard error
// when an interrupt comes while it runs. The commit goes on, so that its
// outcome is known, and the second interrupt gives up the wait for its
// answer (ADR-0043).
type committing struct {
	dialect.Session
	// run is the context of the run, which the first interrupt cancels.
	run    context.Context
	stderr io.Writer
}

// Commit commits, and writes errCommitStarted as soon as the first
// interrupt comes, also when it came just before the commit started.
func (c committing) Commit(ctx context.Context) error {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-c.run.Done():
			select {
			case <-done:
				// The commit had returned.
			default:
				printError(c.stderr, errCommitStarted)
			}
		case <-done:
		}
	}()
	err := c.Session.Commit(ctx)
	close(done)
	// Nothing writes to stderr until the line has been written.
	<-exited
	if err != nil {
		return &commitFailure{err: err}
	}
	return nil
}

// commitFailure is the error of a commit that started. An interrupt did not
// stop it, so this error, and not the interrupt, is what the apply reports.
type commitFailure struct {
	err error
}

func (e *commitFailure) Error() string {
	return e.err.Error()
}

func (e *commitFailure) Unwrap() error {
	return e.err
}

// committed reports whether an apply committed. An apply that had rows to
// write commits, also when the other writes of the apply had done the work
// of all of its statements, so that they wrote no row (ADR-0042).
func committed(r *apply.Result) bool {
	return r.Committed
}
