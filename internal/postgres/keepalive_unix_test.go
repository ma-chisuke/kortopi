//go:build linux || darwin

package postgres

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// keepAlive is the keepalive of a TCP connection, as the operating system
// holds it.
type keepAlive struct {
	on       bool
	idle     time.Duration
	interval time.Duration
	count    int
}

// keepAliveOf reads the keepalive of conn from its socket.
func keepAliveOf(t *testing.T, conn net.Conn) keepAlive {
	t.Helper()
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatalf("the connection is a %T, not a TCP connection", conn)
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ka keepAlive
	var errs [4]error
	if err := raw.Control(func(fd uintptr) {
		var on, idle, interval int
		on, errs[0] = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_KEEPALIVE)
		idle, errs[1] = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, keepAliveIdleOption)
		interval, errs[2] = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL)
		ka.count, errs[3] = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT)
		ka.on, ka.idle, ka.interval = on != 0, time.Duration(idle)*time.Second, time.Duration(interval)*time.Second
	}); err != nil {
		t.Fatal(err)
	}
	for _, err := range errs {
		if err != nil {
			t.Fatalf("read the keepalive of the socket: %v", err)
		}
	}
	return ka
}

// The dialer of a session turns the keepalive of each TCP connection on, as
// the settings say, and refuses the connection of a cancel request once the
// commit has started (ADR-0043). It connects to a listener of the test, not
// to a database.
func TestDialKeepAlive(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		var accepted []net.Conn
		defer func() {
			for _, conn := range accepted {
				_ = conn.Close()
			}
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted = append(accepted, conn)
		}
	}()
	dial := func(t *testing.T, params map[string]string) (*guard, net.Conn, error) {
		t.Helper()
		ka, err := keepAliveConfig(params)
		if err != nil {
			t.Fatal(err)
		}
		g := &guard{keepAlive: ka}
		conn, err := g.dial((&net.Dialer{}).DialContext)(t.Context(), "tcp", ln.Addr().String())
		return g, conn, err
	}
	for _, tt := range []struct {
		params map[string]string
		want   keepAlive
	}{
		{nil, keepAlive{on: true, idle: 10 * time.Second, interval: 5 * time.Second, count: 4}},
		{map[string]string{"keepalives_idle": "30", "keepalives_interval": "2", "keepalives_count": "6"}, keepAlive{on: true, idle: 30 * time.Second, interval: 2 * time.Second, count: 6}},
	} {
		_, conn, err := dial(t, tt.params)
		if err != nil {
			t.Fatal(err)
		}
		if got := keepAliveOf(t, conn); got != tt.want {
			t.Errorf("keepalive with %v = %+v, want %+v", tt.params, got, tt.want)
		}
		_ = conn.Close()
	}

	g, conn, err := dial(t, map[string]string{"keepalives": "0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := keepAliveOf(t, conn); got.on {
		t.Errorf("keepalives 0: the keepalive is on: %+v", got)
	}
	_ = conn.Close()

	// The system refuses an idle time this long, on Linux and on macOS. The
	// connection stays, with the keepalive on: the tuning is best effort, as
	// the dialer of Go does it, and some systems cannot set every value.
	_, conn, err = dial(t, map[string]string{"keepalives_idle": "5000000"})
	if err != nil {
		t.Fatalf("a keepalive that the system refuses made the dial fail: %v", err)
	}
	if got := keepAliveOf(t, conn); !got.on {
		t.Errorf("keepalive after a value that the system refuses = %+v, want it on", got)
	}
	_ = conn.Close()

	g.committing.Store(true)
	if conn, err := g.dial((&net.Dialer{}).DialContext)(t.Context(), "tcp", ln.Addr().String()); !errors.Is(err, errNoCancel) {
		t.Errorf("a dial during the commit = %v, %v, want errNoCancel", conn, err)
	}
}
