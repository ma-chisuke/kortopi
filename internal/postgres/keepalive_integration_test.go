//go:build integration && (linux || darwin)

package postgres

import (
	"context"
	"testing"
	"time"
)

// The connection of a session to the server has the keepalive of kortopi,
// so a server that no longer answers is noticed in about 30 seconds, and the
// settings of libpq for it, as a service file gives them, change it and do
// not reach the server, which would refuse them (ADR-0043). No test cuts the
// network: the time was measured by hand, as the record says.
func TestKeepAliveOfTheSession(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			for _, tt := range []struct {
				params map[string]string
				want   keepAlive
			}{
				{nil, keepAlive{on: true, idle: 10 * time.Second, interval: 5 * time.Second, count: 4}},
				{map[string]string{"keepalives_idle": "20", "keepalives_interval": "3", "keepalives_count": "2"}, keepAlive{on: true, idle: 20 * time.Second, interval: 3 * time.Second, count: 2}},
			} {
				cfg := db.Config.Copy()
				for k, v := range tt.params {
					cfg.RuntimeParams[k] = v
				}
				s, err := ConnectConfig(t.Context(), cfg, Options{})
				if err != nil {
					t.Fatalf("connect with %v: %v", tt.params, err)
				}
				if got := keepAliveOf(t, s.conn.PgConn().Conn()); got != tt.want {
					t.Errorf("keepalive of the session with %v = %+v, want %+v", tt.params, got, tt.want)
				}
				if err := s.Close(context.Background()); err != nil {
					t.Error(err)
				}
			}
		})
	}
}
