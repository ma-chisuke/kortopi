package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// lockedBuffer is a buffer that the command writes and the test reads at the
// same time.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// waitFor waits until the buffer holds s, and fails the test after a
// minute.
func (b *lockedBuffer) waitFor(t *testing.T, s string) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(b.String(), s) {
			return
		}
	}
	t.Fatalf("the output\n%s\ndoes not hold %q", b.String(), s)
}

// The first interrupt cancels the context, the second closes the channel and
// stops catching the signals, so that a third ends the process.
func TestWatchInterrupts(t *testing.T) {
	t.Parallel()

	signals := make(chan os.Signal)
	stopped := make(chan struct{})
	ctx, again := watchInterrupts(signals, func() { close(stopped) })
	if ctx.Err() != nil {
		t.Fatal("the context was cancelled before an interrupt")
	}
	signals <- syscall.SIGTERM
	<-ctx.Done()
	select {
	case <-again:
		t.Fatal("the first interrupt closed the channel of the second")
	case <-stopped:
		t.Fatal("the first interrupt stopped catching the signals")
	default:
	}
	signals <- os.Interrupt
	<-again
	<-stopped
}

// --lock-timeout takes a duration from 1ms to 24h in each command, and
// reaches the options of the session.
func TestLockTimeoutFlag(t *testing.T) {
	t.Parallel()

	for _, d := range []struct {
		value string
		want  time.Duration
	}{
		{"10s", 10 * time.Second},
		{"500ms", 500 * time.Millisecond},
		{"1m30s", 90 * time.Second},
		{"1ms", time.Millisecond},
		{"24h", 24 * time.Hour},
	} {
		p, err := parsePlan([]string{"--lock-timeout", d.value})
		if err != nil || p.lockTimeout != d.want {
			t.Errorf("plan --lock-timeout %s = %v, %v, want %v", d.value, p.lockTimeout, err, d.want)
		}
		a, err := parseApply([]string{"--lock-timeout=" + d.value})
		if err != nil || a.lockTimeout != d.want {
			t.Errorf("apply --lock-timeout %s = %v, %v, want %v", d.value, a.lockTimeout, err, d.want)
		}
		x, err := parseExport([]string{"--lock-timeout", d.value})
		if err != nil || x.lockTimeout != d.want {
			t.Errorf("export --lock-timeout %s = %v, %v, want %v", d.value, x.lockTimeout, err, d.want)
		}
	}
	want := "invalid value for flag --lock-timeout: give a duration from 1ms to 24h, such as 10s, 500ms, or 1m30s"
	for _, value := range []string{"0", "0s", "-1s", "10", "999us", "25h", "soon", ""} {
		for _, parse := range []func([]string) error{
			func(args []string) error { _, err := parsePlan(args); return err },
			func(args []string) error { _, err := parseApply(args); return err },
			func(args []string) error { _, err := parseExport(args); return err },
		} {
			if err := parse([]string{"--lock-timeout", value}); err == nil || err.Error() != want {
				t.Errorf("--lock-timeout %q: %v, want %q", value, err, want)
			}
		}
	}
	if _, err := parsePlan([]string{"--lock-timeout"}); err == nil || err.Error() != "flag needs an argument: --lock-timeout" {
		t.Errorf("--lock-timeout without a value: %v", err)
	}

	// Each command passes the flag and the second interrupt to the session.
	dir := writeFiles(t, twoTables)
	again := make(chan struct{})
	for _, args := range [][]string{
		{"plan", "--dir", dir},
		{"apply", "--dir", dir},
		{"export", "--dir", filepath.Join(t.TempDir(), "out"), "--table", "app.plans"},
	} {
		fd := &fakeDialect{session: &fakeSession{diffs: newPlan, exportable: []declaration.TableName{plans}}}
		var stdout, stderr strings.Builder
		code := run(t.Context(), append(args, "--lock-timeout", "2s"), env{stdout: &stdout, stderr: &stderr, dialects: fd.registry(t), again: again})
		if code != exitOK || fd.opts.LockTimeout != 2*time.Second || fd.opts.GiveUp != (<-chan struct{})(again) {
			t.Errorf("kortopi %s = %d, stderr %q, with the options %+v, want the lock timeout 2s and the channel of the second interrupt",
				args[0], code, stderr.String(), fd.opts)
		}
	}
}

// notEnded is the error of a session whose end the database did not confirm
// after an interrupt.
var notEnded = &dialect.NotEndedError{Message: "the server did not confirm within 5s that the session of the run ended: find it in pg_stat_activity"}

// After an interrupt, when the database did not confirm that the session
// ended, each command says so after the line of the interrupt; an apply,
// whose transaction cannot commit then, still ends with "nothing was
// changed".
func TestInterruptedSessionNotEnded(t *testing.T) {
	t.Parallel()

	line := "kortopi: " + notEnded.Error() + "\n"
	tests := []struct {
		name string
		args func(dir string) []string
		// failing is the call during which the interrupt comes.
		failing string
		// inOpen makes Open fail with the error instead, as when the
		// interrupt comes while an apply waits for another apply.
		inOpen     bool
		wantStderr func(dir string) string
	}{
		{
			name: "plan", args: func(dir string) []string { return []string{"plan", "--dir", dir} }, failing: "Diff app.plans",
			wantStderr: func(string) string { return "kortopi: " + errInterrupted.Error() + "\n" + line },
		},
		{
			name: "apply", args: func(dir string) []string { return []string{"apply", "--dir", dir} }, failing: "Insert app.plans",
			wantStderr: func(string) string {
				return "kortopi: interrupted: the apply was stopped\n" + line + "kortopi: nothing was changed\n"
			},
		},
		{
			name: "apply that waits for another apply", args: func(dir string) []string { return []string{"apply", "--dir", dir} }, inOpen: true,
			wantStderr: func(string) string {
				return "kortopi: interrupted: the apply was stopped\n" + line + "kortopi: nothing was changed\n"
			},
		},
		{
			name: "export", args: func(dir string) []string {
				return []string{"export", "--dir", filepath.Join(dir, "out"), "--table", "app.plans"}
			},
			failing: "Write app.plans",
			wantStderr: func(dir string) string {
				return "kortopi: interrupted: the export was stopped\n" +
					"kortopi: export table app.plans into " + filepath.Join(dir, "out", "app", "plans.csv") + ": context canceled\n" +
					"kortopi: no file was written\n" + line
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeFiles(t, twoTables)
			ctx, cancel := context.WithCancel(t.Context())
			s := &fakeSession{
				diffs: newPlan, exportable: []declaration.TableName{plans}, closeErr: errors.Join(errors.New("roll back"), notEnded),
				fail: map[string]error{tt.failing: context.Canceled},
				after: func(call string) {
					if call == tt.failing {
						cancel()
					}
				},
			}
			fd := &fakeDialect{session: s}
			if tt.inOpen {
				fd.err = errors.Join(fmt.Errorf("wait for another apply on database %q: %w", "shop", context.Canceled), notEnded)
				fd.opened = func(context.Context) { cancel() }
			}
			var stdout, stderr strings.Builder
			code := run(ctx, tt.args(dir), env{stdout: &stdout, stderr: &stderr, dialects: fd.registry(t)})
			if code != exitError || stderr.String() != tt.wantStderr(dir) {
				t.Errorf("kortopi %s = %d, stderr\n%s\nwant %d, stderr\n%s", tt.name, code, stderr.String(), exitError, tt.wantStderr(dir))
			}
		})
	}
}

// An interrupt that comes while the commit runs does not stop it, and
// kortopi says so at once. When the commit then gets its answer, the apply
// ends as if no interrupt had come. The second interrupt gives up the wait,
// and the apply says that the outcome is not known, and nothing else
// (ADR-0043).
func TestCommitInterrupted(t *testing.T) {
	t.Parallel()

	notice := "kortopi: " + errCommitStarted.Error() + "\n"
	unknown := fmt.Errorf("commit the apply: %w (cause: a second interrupt gave up the wait for the answer)", apply.ErrUnknownOutcome)
	for _, gaveUp := range []bool{false, true} {
		t.Run(fmt.Sprintf("second interrupt %v", gaveUp), func(t *testing.T) {
			t.Parallel()

			dir := writeFiles(t, twoTables)
			ctx, cancel := context.WithCancel(t.Context())
			again := make(chan struct{})
			var stdout lockedBuffer
			var stderr lockedBuffer
			fd := &fakeDialect{}
			s := &fakeSession{diffs: newPlan, after: func(call string) {
				if call != "Commit" {
					return
				}
				// The commit waits for its answer, and the first interrupt
				// comes.
				cancel()
				stderr.waitFor(t, notice)
				if gaveUp {
					close(again)
					<-fd.opts.GiveUp
				}
			}}
			if gaveUp {
				s.fail = map[string]error{"Commit": unknown}
			}
			fd.session = s
			code := run(ctx, []string{"apply", "--dir", dir}, env{stdout: &stdout, stderr: &stderr, dialects: fd.registry(t), again: again})

			wantCode, wantStdout, wantStderr := exitOK, planOfGold+"\nApplied: 1 inserted, 0 updated, 0 deleted.\n", notice
			if gaveUp {
				wantCode, wantStdout, wantStderr = exitError, planOfGold, notice+"kortopi: "+unknown.Error()+"\n"
			}
			if code != wantCode || stdout.String() != wantStdout || stderr.String() != wantStderr {
				t.Errorf("kortopi apply = %d, stdout\n%s\nstderr\n%s\nwant %d, stdout\n%s\nstderr\n%s",
					code, stdout.String(), stderr.String(), wantCode, wantStdout, wantStderr)
			}
			if !s.closed {
				t.Error("kortopi apply did not close the session")
			}
		})
	}
}
