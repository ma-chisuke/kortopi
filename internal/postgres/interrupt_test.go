package postgres

import (
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// The keepalive of a connection notices a silent server in about 30 seconds,
// and the settings of libpq for it, which a service file can hold, change it
// as they change the keepalive of libpq (ADR-0043).
func TestKeepAliveConfig(t *testing.T) {
	t.Parallel()

	ours := net.KeepAliveConfig{Enable: true, Idle: 10 * time.Second, Interval: 5 * time.Second, Count: 4}
	if got := ours.Idle + time.Duration(ours.Count)*ours.Interval; got != 30*time.Second {
		t.Errorf("a silent server is noticed after %v, want 30s", got)
	}
	tests := []struct {
		name    string
		params  map[string]string
		want    net.KeepAliveConfig
		wantErr string
	}{
		{name: "none", params: map[string]string{"application_name": "kortopi"}, want: ours},
		{
			name:   "all of them",
			params: map[string]string{"keepalives": "1", "keepalives_idle": "60", "keepalives_interval": "10", "keepalives_count": " 3 ", "application_name": "kortopi"},
			want:   net.KeepAliveConfig{Enable: true, Idle: time.Minute, Interval: 10 * time.Second, Count: 3},
		},
		{name: "off", params: map[string]string{"keepalives": "0"}, want: net.KeepAliveConfig{Interval: 5 * time.Second, Idle: 10 * time.Second, Count: 4}},
		{
			name:   "the values of the system",
			params: map[string]string{"keepalives_idle": "0", "keepalives_interval": "0", "keepalives_count": "0"},
			want:   net.KeepAliveConfig{Enable: true, Idle: -1, Interval: -1, Count: -1},
		},
		{
			name:   "below 0, which libpq takes for 0",
			params: map[string]string{"keepalives": "-1", "keepalives_idle": "-1", "keepalives_interval": "-5", "keepalives_count": "-2"},
			want:   net.KeepAliveConfig{Enable: true, Idle: -1, Interval: -1, Count: -1},
		},
		{
			name: "not a number", params: map[string]string{"keepalives_idle": "10s"},
			wantErr: "read the connection settings: keepalives_idle is not a whole number: correct keepalives_idle in the service that PGSERVICE names",
		},
		{
			name: "empty", params: map[string]string{"keepalives_count": ""},
			wantErr: "read the connection settings: keepalives_count is not a whole number: correct keepalives_count in the service that PGSERVICE names",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			params := maps.Clone(tt.params)
			got, err := keepAliveConfig(params)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("keepAliveConfig(%v) = %v, want the error %q", tt.params, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("keepAliveConfig(%v) = %+v, %v, want %+v", tt.params, got, err, tt.want)
			}
			// The driver would send what is left to the server as settings
			// of the session.
			for name := range params {
				if name != "application_name" {
					t.Errorf("keepAliveConfig left %s among the parameters for the server", name)
				}
			}
		})
	}
}

// The settings of the keepalive in a service file reach the connection, and
// not the server, which would refuse them. Nothing connects. It sets the
// environment, so it does not run in parallel with other tests.
func TestKeepAliveFromTheServiceFile(t *testing.T) {
	clearLibpqEnvironment(t)
	services := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(services, []byte("[deploy]\nhost=db.example.test\ndbname=app\nkeepalives_idle=20\nkeepalives_count=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", services)
	t.Setenv("PGSERVICE", "deploy")
	cfg, err := Options{}.connConfig()
	if err != nil {
		t.Fatal(err)
	}
	got, err := keepAliveConfig(cfg.RuntimeParams)
	if want := (net.KeepAliveConfig{Enable: true, Idle: 20 * time.Second, Interval: 5 * time.Second, Count: 2}); err != nil || got != want {
		t.Errorf("keepalive of the service = %+v, %v, want %+v", got, err, want)
	}
	if _, ok := cfg.RuntimeParams["keepalives_idle"]; ok {
		t.Error("keepalives_idle stays among the settings that go to the server")
	}
}

// Each transaction starts with the lock timeout of --lock-timeout, in whole
// milliseconds, and none without the flag (ADR-0044).
func TestBeginSQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		timeout  time.Duration
		readOnly bool
		want     string
	}{
		{want: "BEGIN ISOLATION LEVEL REPEATABLE READ"},
		{readOnly: true, want: "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"},
		{timeout: 10 * time.Second, want: "BEGIN ISOLATION LEVEL REPEATABLE READ; SET LOCAL lock_timeout = 10000"},
		{timeout: 1500 * time.Millisecond, readOnly: true, want: "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET LOCAL lock_timeout = 1500"},
		{timeout: 1999 * time.Microsecond, want: "BEGIN ISOLATION LEVEL REPEATABLE READ; SET LOCAL lock_timeout = 1"},
	}
	for _, tt := range tests {
		s := &Session{lockTimeout: tt.timeout}
		if got := s.beginSQL(tt.readOnly); got != tt.want {
			t.Errorf("beginSQL(%v) with --lock-timeout %v = %q, want %q", tt.readOnly, tt.timeout, got, tt.want)
		}
	}
}

// The error of a session whose end the server did not confirm says why, and
// names the session by what pg_stat_activity shows, its process ID only when
// kortopi read it in the transaction.
func TestNotEnded(t *testing.T) {
	t.Parallel()

	tail := ": its statement may still run on the server and hold its locks until the server notices that kortopi has gone, " +
		"although its transaction can no longer commit: find the session in pg_stat_activity, where "
	tests := []struct {
		name string
		s    *Session
		want string
	}{
		{
			name: "an apply with its process ID", s: &Session{apply: true, guard: &guard{wait: 5 * time.Second}, application: "kortopi", pid: 386},
			want: "the server did not confirm within 5s that the session of the apply ended" + tail +
				`application_name is "kortopi" and pid is 386, and end it with pg_cancel_backend(386), or with pg_terminate_backend(386) if it does not end`,
		},
		{
			name: "a plan before its process ID was read", s: &Session{guard: &guard{wait: 5 * time.Second}, application: "deploy-rows"},
			want: "the server did not confirm within 5s that the session of the plan ended" + tail +
				`application_name is "deploy-rows", and end it with pg_cancel_backend(pid), or with pg_terminate_backend(pid) if it does not end`,
		},
	}
	for _, tt := range tests {
		err := tt.s.notEnded()
		var notEnded *dialect.NotEndedError
		if !errors.As(err, &notEnded) || err.Error() != tt.want {
			t.Errorf("%s: notEnded() =\n%v\nwant\n%s", tt.name, err, tt.want)
		}
	}
	s := &Session{export: true, guard: &guard{wait: time.Second}, application: "kortopi", pid: 7}
	s.interrupt.gaveUp = true
	if want := "kortopi stopped waiting for the server to end the session of the export" + tail +
		`application_name is "kortopi" and pid is 7, and end it with pg_cancel_backend(7), or with pg_terminate_backend(7) if it does not end`; s.notEnded().Error() != want {
		t.Errorf("notEnded() after the second interrupt =\n%v\nwant\n%s", s.notEnded(), want)
	}
}
