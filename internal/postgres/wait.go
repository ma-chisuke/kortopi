package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// The SQLSTATEs of PostgreSQL for a statement that waited too long: lock_timeout
// ended its wait for a lock, or statement_timeout, or a cancel, ended the
// statement.
const (
	lockNotAvailable = "55P03"
	queryCanceled    = "57014"
)

// waitError is an error of PostgreSQL that ended a wait, with what happened
// in the words of kortopi. It keeps the error of PostgreSQL.
type waitError struct {
	err    error
	advice string
}

func (e *waitError) Error() string {
	return e.err.Error() + ": " + e.advice
}

func (e *waitError) Unwrap() error {
	return e.err
}

// lockLimit names what limited a wait for a lock: --lock-timeout with its
// value, which kortopi sets in each of its transactions (ADR-0044), or else
// the lock_timeout of the session, as the server, the database, the role, or
// PGOPTIONS set it.
func (s *Session) lockLimit() string {
	if s.lockTimeout > 0 {
		return "--lock-timeout " + s.lockTimeout.String()
	}
	return "lock_timeout of the session"
}

// moreTime says how to give a wait for a lock more time: --lock-timeout,
// unless the run has it already.
func (s *Session) moreTime() string {
	if s.lockTimeout > 0 {
		return ""
	}
	return ", or allow the wait more time with --lock-timeout"
}

// explainWait adds to *err what happened when PostgreSQL ended a statement
// that waited: another session held a lock that the statement needed, and
// lock_timeout or statement_timeout ended the wait. A session waits for a
// lock without limit unless one of these settings is set, as --lock-timeout
// sets lock_timeout. A plan and an export change nothing, and a failed apply
// rolls back, so the advice is to run the command again. After an interrupt,
// the cancel is kortopi's own, and the error stays as it is.
func (s *Session) explainWait(ctx context.Context, err *error) {
	var pgErr *pgconn.PgError
	var explained *waitError
	if *err == nil || ctx.Err() != nil || !errors.As(*err, &pgErr) || errors.As(*err, &explained) {
		return
	}
	// The last line of a failed apply says that nothing was changed.
	unchanged := ": nothing was changed"
	if s.apply {
		unchanged = ""
	}
	switch pgErr.Code {
	case lockNotAvailable:
		*err = &waitError{err: *err, advice: fmt.Sprintf("another session holds a lock on a table or on rows that the %s needs, "+
			"and the %s waited for it as long as %s allows%s: run the %s again once that session has released the lock%s",
			s.work(), s.work(), s.lockLimit(), unchanged, s.work(), s.moreTime())}
	case queryCanceled:
		*err = &waitError{err: *err, advice: fmt.Sprintf("the statement was cancelled, by statement_timeout or by another session, "+
			"often while it waited for a lock that another session holds%s: run the %s again once that session has released the lock, or allow the statement more time",
			unchanged, s.work())}
	}
}

// waitForLockError is the error of an apply that waited for the lock of
// kortopi until lock_timeout or statement_timeout ended the wait: another
// apply held it all that time, or the session of an apply that was stopped
// and that the server has not ended yet.
func (s *Session) waitForLockError(database string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == lockNotAvailable || pgErr.Code == queryCanceled) {
		limit := "lock_timeout or statement_timeout of the session"
		if s.lockTimeout > 0 {
			limit = "--lock-timeout " + s.lockTimeout.String()
		}
		return fmt.Errorf("wait for another apply on database %q: %w: the other apply held the lock of kortopi longer than %s allows: "+
			"run the apply again once the other apply has ended, or, when no other apply runs, find the session that holds the lock in pg_stat_activity, "+
			"as an apply that was stopped may leave it, and end it with pg_terminate_backend", database, err, limit)
	}
	return fmt.Errorf("wait for another apply on database %q: %w", database, err)
}
