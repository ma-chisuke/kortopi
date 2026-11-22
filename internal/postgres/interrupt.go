package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// This file holds what a session does when its run is interrupted, and how
// its connection notices a server that no longer answers (ADR-0043).

// cancelWait is how long a session waits, after an interrupt, for the server
// to end the statement that runs and to roll the transaction back. The
// server ends a statement at its next check for an interrupt, which comes
// within milliseconds in a query, a sort, a wait for a lock, or pg_sleep, so
// a server that has not answered after this time is busy elsewhere or out of
// reach.
const cancelWait = 5 * time.Second

// closeTimeout bounds the rollback and the close of a session that Connect
// ends itself, after an error.
const closeTimeout = 10 * time.Second

// The keepalive of the connection: the operating system probes a connection
// that has been idle for keepAliveIdle, every keepAliveInterval, and drops it
// after keepAliveCount probes without an answer, so a server that no longer
// answers, as behind a cut network, is noticed after about 30 seconds of
// silence, instead of the 15 seconds and 9 probes of 15 seconds of the
// runtime, or the two hours of many systems. A server that is slow to answer
// still answers the probes, so a commit that waits for a standby waits as
// long as it takes.
const (
	keepAliveIdle     = 10 * time.Second
	keepAliveInterval = 5 * time.Second
	keepAliveCount    = 4
)

// errGaveUp is the cause of a commit whose answer kortopi stopped waiting
// for.
var errGaveUp = errors.New("a second interrupt gave up the wait for the answer")

// errNoCancel is the error of a cancel request during a commit, which
// kortopi does not send.
var errNoCancel = errors.New("kortopi sends no cancel request during a commit")

// guard is what the connection of a session, its dialer, and the watcher of
// its contexts share with the session.
type guard struct {
	// wait is how long a cancelled statement may take to end on the server.
	wait time.Duration
	// keepAlive is the keepalive of the connection.
	keepAlive net.KeepAliveConfig
	// noCancel is true while a done context must end a wait at once, with no
	// cancel request: while the session closes after an interrupt, and while
	// the commit runs.
	noCancel atomic.Bool
	// committing is true from the start of the commit: no cancel request
	// leaves kortopi then, so a commit that kortopi stops waiting for runs on
	// the server to its end.
	committing atomic.Bool
	// mu orders the deadlines that the watcher and giveUp set on the
	// connection, and gaveUp is true after giveUp, whose deadline stays.
	mu     sync.Mutex
	gaveUp bool
}

// giveUp ends every wait of the connection conn for the server, at once and
// for good: the second interrupt came.
func (g *guard) giveUp(conn net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gaveUp = true
	// A deadline that has passed ends the read that waits for the server,
	// and every later one.
	_ = conn.SetDeadline(time.Now())
}

// handler returns the watcher of the contexts of conn, which pgx calls when
// the context of a statement is done (BuildContextWatcherHandler).
func (g *guard) handler(conn *pgconn.PgConn) ctxwatch.Handler {
	return &watcher{
		guard:    g,
		cancel:   &pgconn.CancelRequestContextWatcherHandler{Conn: conn, DeadlineDelay: g.wait},
		deadline: &pgconn.DeadlineContextWatcherHandler{Conn: conn.Conn()},
	}
}

// dial returns the dialer of the driver for a session: it dials as the
// default dialer of the driver does, with timeout, the connect_timeout of
// the settings, sets the keepalive of each TCP connection as keepAlive says,
// as far as the system lets it, and refuses the connection of a cancel
// request during the commit. The driver dials for the session and for each
// cancel request.
//
// The dialer of Go would set its own keepalive, 15 seconds, 15 seconds, and
// 9 probes, on each socket before kortopi sets its own, and a value that
// kortopi leaves, as a 0 of the settings of libpq does, would then be the
// one of Go, not the one of the system: so its own keepalive is off.
func (g *guard) dial(timeout time.Duration) pgconn.DialFunc {
	d := &net.Dialer{Timeout: timeout, KeepAlive: -1}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if g.committing.Load() {
			return nil, errNoCancel
		}
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			tuneKeepAlive(tcp, g.keepAlive)
		}
		return conn, nil
	}
}

// tuneKeepAlive sets the keepalive of conn to ka, as far as the system lets
// it, and keeps the connection whatever it refuses, as the dialer of Go does.
// A system can refuse one value, as Windows before 10 version 1709 refuses
// the count of probes, and Linux and macOS an idle time of millions of
// seconds: kortopi then sets what it can, and at least turns the keepalive
// on or off.
func tuneKeepAlive(conn *net.TCPConn, ka net.KeepAliveConfig) {
	if conn.SetKeepAliveConfig(ka) == nil {
		return
	}
	withoutCount := ka
	withoutCount.Count = -1
	if conn.SetKeepAliveConfig(withoutCount) == nil {
		return
	}
	_ = conn.SetKeepAlive(ka.Enable)
}

// watcher ends the wait of a statement whose context is done. Unless the
// guard says otherwise, it asks the server to cancel the statement, with the
// cancel request of the protocol, and waits up to guard.wait for the answer,
// so that the server ends the statement and its locks (ADR-0043). The
// default of the driver only stops reading, and the server goes on.
type watcher struct {
	guard    *guard
	cancel   *pgconn.CancelRequestContextWatcherHandler
	deadline *pgconn.DeadlineContextWatcherHandler
	// active is the handler that HandleCancel chose. The context watcher of
	// the driver calls HandleUnwatchAfterCancel only after HandleCancel has
	// returned.
	active ctxwatch.Handler
}

// HandleCancel ends the wait of a statement whose context is done: with a
// cancel request and a deadline guard.wait away, or, when the guard says so,
// with a deadline that has passed.
func (w *watcher) HandleCancel(ctx context.Context) {
	w.guard.mu.Lock()
	defer w.guard.mu.Unlock()
	w.active = w.cancel
	if w.guard.gaveUp || w.guard.noCancel.Load() {
		w.active = w.deadline
	}
	w.active.HandleCancel(ctx)
}

// HandleUnwatchAfterCancel clears the deadline of HandleCancel once the
// statement has returned, but the deadline of the second interrupt.
func (w *watcher) HandleUnwatchAfterCancel() {
	// This clears the deadline, and may wait for the cancel request to go
	// out, so the guard is not locked meanwhile.
	w.active.HandleUnwatchAfterCancel()
	w.guard.mu.Lock()
	defer w.guard.mu.Unlock()
	if w.guard.gaveUp {
		_ = w.deadline.Conn.SetDeadline(time.Now())
	}
}

// keepAliveConfig returns the keepalive of a connection whose parameters are
// params, and removes from params the settings of libpq for it: keepalives,
// keepalives_idle, keepalives_interval, and keepalives_count. libpq takes
// them from a connection string or a service file, and so does the driver,
// which does not know them and would pass them to the server as settings of
// the session, which it refuses. kortopi applies them itself, as libpq
// does: keepalives turns the keepalive off when it is 0, and on otherwise,
// and for the others 0 keeps the value of the system, and so does a negative
// value, which libpq takes for 0. A value that is not a whole number is an
// error, as in libpq.
func keepAliveConfig(params map[string]string) (net.KeepAliveConfig, error) {
	ka := net.KeepAliveConfig{Enable: true, Idle: keepAliveIdle, Interval: keepAliveInterval, Count: keepAliveCount}
	// A negative field leaves the option of the socket as it is, which is
	// the value of the system, since kortopi dials with the keepalive of Go
	// off (guard.dial).
	seconds := func(n int) time.Duration {
		if n <= 0 {
			return -1
		}
		return time.Duration(n) * time.Second
	}
	for _, p := range [...]struct {
		name string
		set  func(n int)
	}{
		{"keepalives", func(n int) { ka.Enable = n != 0 }},
		{"keepalives_idle", func(n int) { ka.Idle = seconds(n) }},
		{"keepalives_interval", func(n int) { ka.Interval = seconds(n) }},
		{"keepalives_count", func(n int) {
			ka.Count = n
			if n <= 0 {
				ka.Count = -1
			}
		}},
	} {
		v, ok := params[p.name]
		if !ok {
			continue
		}
		delete(params, p.name)
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return net.KeepAliveConfig{}, fmt.Errorf("read the connection settings: %s is not a whole number: "+
				"correct %s in the service that PGSERVICE names", p.name, p.name)
		}
		p.set(n)
	}
	return ka, nil
}

// interruption is what a session knows of the interrupt of its run, which
// cancels the context of Connect.
type interruption struct {
	mu sync.Mutex
	// endBy is when the session stops waiting for the server, after the
	// interrupt: zero until it comes.
	endBy time.Time
	// gaveUp is true after the second interrupt.
	gaveUp bool
	// closed is closed once the session has closed.
	closed chan struct{}
}

// watchInterrupt makes the session notice the interrupt of its run, which
// cancels run, and the second interrupt, which closes giveUp. The session
// keeps the time by which it stops waiting for the server, and the second
// interrupt ends every read and write of the connection at once.
func (s *Session) watchInterrupt(run context.Context, giveUp <-chan struct{}) {
	s.run = run
	s.interrupt.closed = make(chan struct{})
	s.stopWatch = context.AfterFunc(run, func() {
		s.interrupt.mu.Lock()
		s.interrupt.endBy = time.Now().Add(s.guard.wait)
		s.interrupt.mu.Unlock()
		if giveUp == nil {
			return
		}
		select {
		case <-giveUp:
		case <-s.interrupt.closed:
			return
		}
		s.interrupt.mu.Lock()
		s.interrupt.gaveUp = true
		s.interrupt.mu.Unlock()
		s.guard.giveUp(s.conn.PgConn().Conn())
	})
}

// interrupted reports whether an interrupt stopped the run of the session,
// and if so, by when the session stops waiting for the server.
func (s *Session) interrupted() (bool, time.Time) {
	if s.run == nil || s.run.Err() == nil {
		return false, time.Time{}
	}
	s.interrupt.mu.Lock()
	defer s.interrupt.mu.Unlock()
	if s.interrupt.endBy.IsZero() {
		// The interrupt came, and the function that keeps its time has not
		// run yet.
		return true, time.Now().Add(s.guard.wait)
	}
	return true, s.interrupt.endBy
}

// gaveUp reports whether the second interrupt came.
func (s *Session) gaveUp() bool {
	s.interrupt.mu.Lock()
	defer s.interrupt.mu.Unlock()
	return s.interrupt.gaveUp
}

// notEnded returns the error of a session that an interrupt stopped, when
// the server did not confirm that its work ended: within guard.wait, or
// before the second interrupt. It names the session as pg_stat_activity
// shows it, by its application_name and, when kortopi read it in the
// transaction, its process ID.
func (s *Session) notEnded() error {
	why := fmt.Sprintf("the server did not confirm within %s that the session of the %s ended", s.guard.wait, s.work())
	if s.gaveUp() {
		why = fmt.Sprintf("kortopi stopped waiting for the server to end the session of the %s", s.work())
	}
	where := fmt.Sprintf("application_name is %q", s.application)
	end := "pg_cancel_backend(pid), or with pg_terminate_backend(pid) if it does not end"
	if s.pid != 0 {
		where += fmt.Sprintf(" and pid is %d", s.pid)
		end = fmt.Sprintf("pg_cancel_backend(%d), or with pg_terminate_backend(%d) if it does not end", s.pid, s.pid)
	}
	return &dialect.NotEndedError{Message: why + ": its statement may still run on the server and hold its locks until the server notices that kortopi has gone, " +
		"although its transaction can no longer commit: find the session in pg_stat_activity, where " + where + ", and end it with " + end}
}
