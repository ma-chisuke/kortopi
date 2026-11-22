package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// The end of a wait for a lock, by lock_timeout or by statement_timeout,
// says what happened in the words of kortopi, and keeps the error of
// PostgreSQL. Other errors, and the cancel of an interrupt, stay as they are.
func TestExplainWait(t *testing.T) {
	lock := &pgconn.PgError{Severity: "ERROR", Code: lockNotAvailable, Message: "canceling statement due to lock timeout"}
	cancel := &pgconn.PgError{Severity: "ERROR", Code: queryCanceled, Message: "canceling statement due to statement timeout"}
	other := &pgconn.PgError{Severity: "ERROR", Code: "22P02", Message: "invalid input syntax"}
	wrap := func(err error) error { return fmt.Errorf("create the temporary table of table s.t: %w", err) }
	done, stop := context.WithCancel(t.Context())
	stop()

	tests := []struct {
		name string
		s    *Session
		ctx  context.Context
		err  error
		want string
	}{
		{
			name: "lock timeout in a plan", s: &Session{}, ctx: t.Context(), err: wrap(lock),
			want: "create the temporary table of table s.t: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): " +
				"another session holds a lock on a table or on rows that the plan needs, and the plan waited for it as long as lock_timeout of the session allows: " +
				"nothing was changed: run the plan again once that session has released the lock, or allow the wait more time with --lock-timeout",
		},
		{
			name: "lock timeout in an apply, whose last line says that nothing was changed", s: &Session{apply: true}, ctx: t.Context(), err: wrap(lock),
			want: "create the temporary table of table s.t: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): " +
				"another session holds a lock on a table or on rows that the apply needs, and the apply waited for it as long as lock_timeout of the session allows: " +
				"run the apply again once that session has released the lock, or allow the wait more time with --lock-timeout",
		},
		{
			name: "lock timeout of --lock-timeout", s: &Session{apply: true, lockTimeout: 1500 * time.Millisecond}, ctx: t.Context(), err: wrap(lock),
			want: "create the temporary table of table s.t: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): " +
				"another session holds a lock on a table or on rows that the apply needs, and the apply waited for it as long as --lock-timeout 1.5s allows: " +
				"run the apply again once that session has released the lock",
		},
		{
			name: "statement timeout in an export", s: &Session{export: true}, ctx: t.Context(), err: wrap(cancel),
			want: "create the temporary table of table s.t: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): " +
				"the statement was cancelled, by statement_timeout or by another session, often while it waited for a lock that another session holds: " +
				"nothing was changed: run the export again once that session has released the lock, or allow the statement more time",
		},
		{name: "after an interrupt", s: &Session{}, ctx: done, err: wrap(cancel), want: wrap(cancel).Error()},
		{name: "another error", s: &Session{}, ctx: t.Context(), err: wrap(other), want: wrap(other).Error()},
		{name: "no error of PostgreSQL", s: &Session{}, ctx: t.Context(), err: errors.New("conn closed"), want: "conn closed"},
	}
	for _, tt := range tests {
		err := tt.err
		tt.s.explainWait(tt.ctx, &err)
		if err.Error() != tt.want {
			t.Errorf("%s: explainWait =\n%s\nwant\n%s", tt.name, err, tt.want)
		}
		if !errors.Is(err, tt.err) {
			t.Errorf("%s: explainWait lost the error %v", tt.name, tt.err)
		}
		// A second call, as from an outer method, adds nothing.
		again := err
		tt.s.explainWait(tt.ctx, &again)
		if again.Error() != err.Error() {
			t.Errorf("%s: a second explainWait = %s", tt.name, again)
		}
	}
	var none error
	(&Session{}).explainWait(t.Context(), &none)
	if none != nil {
		t.Errorf("explainWait of no error = %v", none)
	}

	stopped := "run the apply again once the other apply has ended, or, when no other apply runs, find the session that holds the lock in pg_stat_activity, " +
		"as an apply that was stopped may leave it, and end it with pg_terminate_backend"
	for _, tt := range []struct {
		s     *Session
		limit string
	}{
		{&Session{apply: true}, "lock_timeout or statement_timeout of the session"},
		{&Session{apply: true, lockTimeout: 3 * time.Second}, "--lock-timeout 3s"},
	} {
		err := tt.s.waitForLockError("app", lock)
		want := `wait for another apply on database "app": ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): ` +
			"the other apply held the lock of kortopi longer than " + tt.limit + " allows: " + stopped
		if err.Error() != want || !errors.Is(err, lock) {
			t.Errorf("waitForLockError =\n%s\nwant\n%s", err, want)
		}
	}
	if err := (&Session{apply: true}).waitForLockError("app", other); err.Error() != `wait for another apply on database "app": `+other.Error() {
		t.Errorf("waitForLockError of another error = %s", err)
	}
}
