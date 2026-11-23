package postgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// The comparison of the schema after the deletions finds every object that
// another session added, removed, or changed, and the error names the first
// and counts the others (ADR-0048). The first is an object that a statement
// names: an attach or a detach of a partition comes before the objects that
// the partition brings or takes away, which are counted, not named, and an
// object comes before those that PostgreSQL made from it on partitions.
func TestSchemaChanges(t *testing.T) {
	orders := declaration.TableName{Schema: "app", Name: "orders"}
	plans := declaration.TableName{Schema: "app", Name: "plans"}
	part := declaration.TableName{Schema: "app", Name: "orders_2"}
	key := func(name, def string, derived bool) schemaObject {
		o := schemaObject{kind: schemaForeignKey, relid: 10, other: 20, name: name, def: def, derived: derived, table: orders, otherTable: plans}
		if derived {
			o.relid, o.table = 12, part
		}
		return o
	}
	// partKey is the own foreign key of the partition, which it had before
	// an attach, and keeps after a detach.
	partKey := schemaObject{kind: schemaForeignKey, relid: 12, other: 20, name: "orders_2_q_fkey", def: "q", table: part, otherTable: plans}
	trigger := func(def string) schemaObject {
		return schemaObject{kind: schemaTrigger, relid: 10, name: "audit", def: def, table: orders}
	}
	rule := func(def string) schemaObject {
		return schemaObject{kind: schemaRule, relid: 10, name: "seen", def: def, table: orders}
	}
	partition := func(def string) schemaObject {
		return schemaObject{kind: schemaPartition, relid: 10, other: 12, def: def, table: orders, otherTable: part}
	}
	reading := func(tables []uint32, os ...schemaObject) schemaReading {
		r := schemaReading{server: "s", objects: make(map[string]schemaObject, len(os)), tables: make(map[uint32]bool, len(tables))}
		for _, o := range os {
			r.objects[o.key()] = o
		}
		for _, t := range tables {
			r.tables[t] = true
		}
		return r
	}
	one := []uint32{10, 20}
	two := []uint32{10, 12, 20}
	// A partitioned table attached as a partition of app.orders, with a
	// partition of its own, and the tables of the comparison with them.
	aOrders := declaration.TableName{Schema: "app", Name: "a_orders"}
	aOrders1 := declaration.TableName{Schema: "app", Name: "a_orders_1"}
	upper := schemaObject{kind: schemaPartition, relid: 10, other: 30, def: "(f)", table: orders, otherTable: aOrders}
	lower := schemaObject{kind: schemaPartition, relid: 30, other: 31, def: "(f)", table: aOrders, otherTable: aOrders1}
	nested := []uint32{10, 20, 30, 31}
	tail := " after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
		"run the apply again, and make schema changes before an apply, not while it runs"
	more := func(n string) string {
		return ", and made " + n + " to the schema of the tables that the deletions reach"
	}
	for _, tt := range []struct {
		name          string
		snapshot, now schemaReading
		want          string
	}{
		{name: "the same schema", snapshot: reading(one, key("orders_plan_fkey", "c", false), trigger("t")), now: reading(one, key("orders_plan_fkey", "c", false), trigger("t"))},
		{
			name: "a foreign key added", snapshot: reading(one, key("orders_plan_fkey", "c", false)),
			now:  reading(one, key("orders_plan_fkey", "c", false), key("orders_next_fkey", "n", false)),
			want: `another session changed the schema while the apply ran: it added or renamed foreign key "orders_next_fkey" of table app.orders, which references table app.plans,` + tail,
		},
		{
			name: "a foreign key dropped", snapshot: reading(one, key("orders_plan_fkey", "c", false)), now: reading(one),
			want: `another session changed the schema while the apply ran: it dropped or renamed foreign key "orders_plan_fkey" of table app.orders, which references table app.plans,` + tail,
		},
		{
			name: "the triggers of a foreign key disabled", snapshot: reading(one, key("orders_plan_fkey", "c O", false)), now: reading(one, key("orders_plan_fkey", "c D", false)),
			want: `another session changed the schema while the apply ran: it changed foreign key "orders_plan_fkey" of table app.orders, which references table app.plans, ` +
				"or enabled or disabled its triggers," + tail,
		},
		{
			name: "a trigger created", snapshot: reading(one), now: reading(one, trigger("t")),
			want: `another session changed the schema while the apply ran: it created or renamed trigger "audit" of table app.orders` + tail,
		},
		{
			name: "a trigger disabled", snapshot: reading(one, trigger("t O")), now: reading(one, trigger("t D")),
			want: `another session changed the schema while the apply ran: it changed trigger "audit" of table app.orders, or enabled or disabled it` + tail,
		},
		{
			name: "a rule dropped", snapshot: reading(one, rule("r")), now: reading(one),
			want: `another session changed the schema while the apply ran: it dropped or renamed rule "seen" of table app.orders` + tail,
		},
		{
			name: "a partition attached, with the key that PostgreSQL gives it and the key that it had", snapshot: reading(one, key("orders_plan_fkey", "c", false)),
			now: reading(two, key("orders_plan_fkey", "c", false), key("orders_plan_fkey", "c", true), partKey, partition("(f)")),
			want: "another session changed the schema while the apply ran: it attached or created partition app.orders_2 of table app.orders after the apply began" +
				more("2 more changes") + ", so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs",
		},
		{
			name: "a partition detached, with its keys", snapshot: reading(two, key("orders_plan_fkey", "c", false), key("orders_plan_fkey", "c", true), partKey, partition("(f)")),
			now: reading(one, key("orders_plan_fkey", "c", false)),
			want: "another session changed the schema while the apply ran: it detached or dropped partition app.orders_2 of table app.orders after the apply began" +
				more("2 more changes") + ", so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs",
		},
		{
			name: "a trigger renamed", snapshot: reading(one, trigger("t")),
			now: reading(one, schemaObject{kind: schemaTrigger, relid: 10, name: "audit_rows", def: "t", table: orders}),
			want: `another session changed the schema while the apply ran: it dropped or renamed trigger "audit" of table app.orders after the apply began` +
				more("1 more change") + ", so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs",
		},
		{
			name: "a deferrable unique key added, named by the constraint", snapshot: reading(one),
			now:  reading(one, schemaObject{kind: schemaConstraint, relid: 10, name: "orders_code_key", def: "u", table: orders}),
			want: `another session changed the schema while the apply ran: it added or renamed constraint "orders_code_key" of table app.orders` + tail,
		},
		{
			name:     "a deferrable unique key changed",
			snapshot: reading(one, schemaObject{kind: schemaConstraint, relid: 10, name: "orders_code_key", def: "u t", table: orders}),
			now:      reading(one, schemaObject{kind: schemaConstraint, relid: 10, name: "orders_code_key", def: "u f", table: orders}),
			want: `another session changed the schema while the apply ran: it changed constraint "orders_code_key" of table app.orders, ` +
				"or enabled or disabled its triggers" + tail,
		},
		{
			name: "a partitioned partition attached, with its own partition", snapshot: reading(one), now: reading(nested, upper, lower),
			want: "another session changed the schema while the apply ran: it attached or created partition app.a_orders of table app.orders after the apply began" +
				more("1 more change") + ", so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs",
		},
		{
			name: "a partitioned partition detached, with its own partition", snapshot: reading(nested, upper, lower), now: reading(one),
			want: "another session changed the schema while the apply ran: it detached or dropped partition app.a_orders of table app.orders after the apply began" +
				more("1 more change") + ", so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs",
		},
		{
			name: "a partition that began to detach", snapshot: reading(two, partKey, partition("(f)")), now: reading(two, partKey, partition("(t)")),
			want: "another session changed the schema while the apply ran: it began to detach partition app.orders_2 of table app.orders" + tail,
		},
		{
			name: "a key on a partitioned table, with the keys that PostgreSQL made on its partitions", snapshot: reading(two, partition("(f)")),
			now: reading(two, partition("(f)"), key("orders_plan_fkey", "c", true), key("orders_plan_fkey", "c", false)),
			want: `another session changed the schema while the apply ran: it added or renamed foreign key "orders_plan_fkey" of table app.orders, which references table app.plans,` +
				" after the apply began" + more("1 more change") + ", so the deletions may have done other than the plan shows: the apply stops before its commit, " +
				"and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := compareSchema(tt.snapshot, tt.now)
			if tt.want == "" {
				if err != nil {
					t.Errorf("compareSchema = %v, want nil", err)
				}
				return
			}
			var schemaErr *schemaError
			if err == nil || err.Error() != tt.want || !errors.As(err, &schemaErr) {
				t.Errorf("error:\n%v\nwant:\n%s", err, tt.want)
			}
		})
	}
}

// The second session must reach the server and the database of the apply:
// another one, which a proxy, a connection pooler, or a list of hosts can
// give it, stops the apply before the objects are compared (ADR-0048).
func TestCompareSchemaOnAnotherServer(t *testing.T) {
	same := schemaReading{server: "2026-11-22 10:00:00+00 app", objects: map[string]schemaObject{}, tables: map[uint32]bool{}}
	other := same
	other.server = "2026-11-21 08:00:00+00 app"
	if err := compareSchema(same, same); err != nil {
		t.Errorf("the same server: %v", err)
	}
	want := "the check after the deletions reads the schema of the tables that they reach through a second connection, " +
		"which reached another server or another database than the apply, so it cannot compare the schema: the apply stops before its commit, and nothing was changed: " +
		"connect kortopi to the server itself, with one host rather than a list of hosts, and not through a proxy or a connection pooler that may send a new connection to another server, " +
		"or apply without deleting rows"
	if err := compareSchema(same, other); err == nil || err.Error() != want {
		t.Errorf("another server: %v\nwant %s", err, want)
	}
}

// fakeConn is a connection with a remote address, for newestConfig.
type fakeConn struct {
	net.Conn
	remote net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr {
	return c.remote
}

// The second session reaches the address of the connection of the apply,
// with the host of the settings that has its port, and the TLS of that
// host, also when the settings name several hosts (ADR-0048).
func TestNewestConfig(t *testing.T) {
	cfg, err := pgx.ParseConfig("host=db1.example.test,db2.example.test port=5432,6432 user=u dbname=d sslmode=disable target_session_attrs=any")
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 6432}
	got, pinned := newestConfig(cfg, fakeConn{remote: remote})
	if pinned != "192.0.2.7:6432" || got.Host != "db2.example.test" || got.Port != 6432 || len(got.Fallbacks) != 0 || got.TLSConfig != nil {
		t.Errorf("newestConfig = host %s port %d, %d fallbacks, TLS %v, pinned %q; want db2.example.test 6432, none, no TLS, 192.0.2.7:6432",
			got.Host, got.Port, len(got.Fallbacks), got.TLSConfig, pinned)
	}
	if ips, err := got.LookupFunc(context.Background(), "db2.example.test"); err != nil || len(ips) != 1 || ips[0] != "192.0.2.7" {
		t.Errorf("lookup = %v, %v; want the address of the apply alone", ips, err)
	}
	// The settings are those of the apply, untouched.
	if cfg.Host != "db1.example.test" || len(cfg.Fallbacks) != 1 {
		t.Errorf("the settings of the apply changed: host %s, %d fallbacks", cfg.Host, len(cfg.Fallbacks))
	}

	// With TLS, the server name of TLS tells the hosts apart.
	tlsCfg, err := pgx.ParseConfig("host=db1.example.test,db2.example.test port=5432 user=u dbname=d sslmode=require")
	if err != nil {
		t.Fatal(err)
	}
	client := handshake(t, fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.8"), Port: 5432}}, "db2.example.test")
	if got, _ := newestConfig(tlsCfg, client); got.Host != "db2.example.test" || got.TLSConfig == nil || len(got.Fallbacks) != 0 {
		t.Errorf("newestConfig with TLS = host %s, TLS %v, %d fallbacks; want db2.example.test with TLS alone", got.Host, got.TLSConfig != nil, len(got.Fallbacks))
	}

	// TLS sends no server name for a host given as an address, so the
	// address that the apply reached tells such hosts apart, and the
	// certificate is verified against that address.
	ipCfg, err := pgx.ParseConfig("host=192.0.2.1,192.0.2.2 port=5432 user=u dbname=d sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	client = handshake(t, fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.2"), Port: 5432}}, "192.0.2.2")
	if got, _ := newestConfig(ipCfg, client); got.Host != "192.0.2.2" || got.TLSConfig == nil || got.TLSConfig.ServerName != "192.0.2.2" || len(got.Fallbacks) != 0 {
		t.Errorf("newestConfig with TLS to addresses = host %s, %d fallbacks; want 192.0.2.2 alone, verified against it", got.Host, len(got.Fallbacks))
	}

	// A socket of Unix and a host of TCP on one port: a connection over TCP
	// takes the host of TCP.
	mixedCfg, err := pgx.ParseConfig("host=/run/one,db.example.test port=5432 user=u dbname=d sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := newestConfig(mixedCfg, fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5432}}); got.Host != "db.example.test" || len(got.Fallbacks) != 0 {
		t.Errorf("newestConfig over TCP with a socket of Unix first = host %s, %d fallbacks; want db.example.test alone", got.Host, len(got.Fallbacks))
	}

	// A host given as an address that is not the one that the apply
	// reached is not taken, also before a host name.
	nameCfg, err := pgx.ParseConfig("host=192.0.2.1,db.example.test port=5432 user=u dbname=d sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := newestConfig(nameCfg, fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5432}}); got.Host != "db.example.test" || len(got.Fallbacks) != 0 {
		t.Errorf("newestConfig with another address first = host %s, %d fallbacks; want db.example.test alone", got.Host, len(got.Fallbacks))
	}

	// Over a socket of Unix, the host whose socket the apply reached is the
	// only host, and no address of TCP is pinned.
	unixCfg, err := pgx.ParseConfig("host=/run/one,/run/two port=5432 user=u dbname=d sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	got, pinned = newestConfig(unixCfg, fakeConn{remote: &net.UnixAddr{Name: "/run/two/.s.PGSQL.5432", Net: "unix"}})
	if pinned != "" || got.Host != "/run/two" || len(got.Fallbacks) != 0 {
		t.Errorf("newestConfig over a socket of Unix = host %s, %d fallbacks, pinned %q; want /run/two alone, nothing pinned", got.Host, len(got.Fallbacks), pinned)
	}
}

// The remote address of a connection over a socket of Unix is the path of
// the socket, which newestConfig matches with the hosts of the settings.
func TestRemoteAddressOfAUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	network, path := pgconn.NetworkAddress(dir, 5432)
	ln, err := new(net.ListenConfig).Listen(t.Context(), network, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	conn, err := new(net.Dialer).DialContext(t.Context(), network, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cfg, err := pgx.ParseConfig("host=/run/other," + dir + " port=5432 user=u dbname=d sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := newestConfig(cfg, conn); got.Host != dir || len(got.Fallbacks) != 0 {
		t.Errorf("newestConfig = host %s, %d fallbacks; want %s alone (remote address %s)", got.Host, len(got.Fallbacks), dir, conn.RemoteAddr())
	}
}

// handshake returns the client side of a TLS connection over a pipe, after
// a handshake with the server name name, a host name or an address, with
// the remote address of conn.
func handshake(t *testing.T, conn fakeConn, name string) *tls.Conn {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	if ip := net.ParseIP(name); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{name}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	clientEnd, serverEnd := net.Pipe()
	t.Cleanup(func() { _ = clientEnd.Close(); _ = serverEnd.Close() })
	server := tls.Server(serverEnd, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
	go func() { _ = server.HandshakeContext(context.Background()) }()
	conn.Conn = clientEnd
	client := tls.Client(conn, &tls.Config{ServerName: name, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err := client.HandshakeContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	return client
}

// Every dial of the second session reaches the pinned address, whatever
// address the driver asks for.
func TestDialPinned(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan struct{})
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
			close(accepted)
		}
	}()
	g := &guard{pinned: ln.Addr().String(), keepAlive: net.KeepAliveConfig{Enable: true}}
	conn, err := g.dial(time.Second)(context.Background(), "tcp", "192.0.2.1:1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("the pinned address got no connection")
	}
}

// The second session waits for a lock 5 seconds at most, and connects within
// 10 seconds, or within the settings when they are shorter (ADR-0048).
func TestNewestBounds(t *testing.T) {
	cfg, err := pgx.ParseConfig("host=db.example.test user=u dbname=d")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		lockTimeout, connectTimeout time.Duration
		lock, connect               time.Duration
		lockText, connectText       string
	}{
		{lock: 5 * time.Second, connect: 10 * time.Second, lockText: "5s, the most that the check waits", connectText: "10s, the most that the check waits"},
		{lockTimeout: time.Minute, connectTimeout: time.Minute, lock: 5 * time.Second, connect: 10 * time.Second,
			lockText: "5s, the most that the check waits", connectText: "10s, the most that the check waits"},
		{lockTimeout: 300 * time.Millisecond, connectTimeout: 3 * time.Second, lock: 300 * time.Millisecond, connect: 3 * time.Second,
			lockText: "300ms, as --lock-timeout allows", connectText: "3s, as connect_timeout allows"},
	} {
		c := cfg.Copy()
		c.ConnectTimeout = tt.connectTimeout
		s := &Session{lockTimeout: tt.lockTimeout, config: c}
		lock, connect := s.newestBounds()
		if lock != tt.lock || connect != tt.connect {
			t.Errorf("bounds with --lock-timeout %v and connect_timeout %v = %v, %v; want %v, %v", tt.lockTimeout, tt.connectTimeout, lock, connect, tt.lock, tt.connect)
		}
		if got := boundText(lock, checkLockWait, "--lock-timeout"); got != tt.lockText {
			t.Errorf("lock bound = %q, want %q", got, tt.lockText)
		}
		if got := boundText(connect, checkConnectWait, "connect_timeout"); got != tt.connectText {
			t.Errorf("connect bound = %q, want %q", got, tt.connectText)
		}
	}
}

// The close of an apply names its own session first, and its second session,
// as what it is, in a line of its own (ADR-0048).
func TestJoinNotEnded(t *testing.T) {
	own := &dialect.NotEndedError{Message: "the server did not confirm within 5s that the session of the apply ended: ..."}
	check := &dialect.NotEndedError{Message: "the server did not confirm within 5s that the read-only session of the check after the deletions ended: ..."}
	var notEnded *dialect.NotEndedError
	if err := joinNotEnded(own, check); !errors.As(err, &notEnded) || notEnded.Message != own.Message+"\n"+check.Message {
		t.Errorf("both = %v", err)
	}
	if err := joinNotEnded(nil, check); !errors.As(err, &notEnded) || notEnded.Message != check.Message {
		t.Errorf("the check alone = %v", err)
	}
	if err := joinNotEnded(own, errors.New("close the connection")); !errors.Is(err, own) || err.Error() != own.Error() {
		t.Errorf("another error of the second session = %v, want the error of the apply alone", err)
	}
	if err := joinNotEnded(nil, nil); err != nil {
		t.Errorf("none = %v", err)
	}
}

// A constraint that fails when the check after the deletions runs the
// constraints deferred to the commit gets advice that is true at that
// point, where every write of the apply has run, its deletions among them,
// for each kind of constraint that can fail there (ADR-0048).
func TestDeferredError(t *testing.T) {
	after := "deferred to the commit failed when the apply checked it after every write, its deletions among them: "
	for _, tt := range []struct {
		code, want string
	}{
		{
			code: "23503",
			want: "a foreign key " + after + "a row refers to a row that does not exist, or a row that the apply deleted is still referenced: " +
				"declare the referenced row, or keep in the declaration a row that other rows reference",
		},
		{
			code: "23505",
			want: "a unique key or a primary key " + after + "two rows hold the same value of the key: change the declared value, or the row of the database that holds it",
		},
		{
			code: "23P01",
			want: "an exclusion constraint " + after + "two rows conflict on it: change the declared values, or the row of the database that conflicts with them",
		},
		{
			code: "P0001",
			want: "a constraint trigger deferred to the commit failed when the apply ran it after every write, its deletions among them: " +
				"check the constraint triggers of the tables that the apply writes, and what their functions read and write",
		},
		{code: "40P01", want: ErrConflict.Error()},
	} {
		t.Run(tt.code, func(t *testing.T) {
			pgErr := &pgconn.PgError{Code: tt.code, Message: "m"}
			err := deferredError(deferredWhat, pgErr)
			if want := deferredWhat + ": m: " + tt.want; err.Error() != want {
				t.Errorf("error:\n%s\nwant:\n%s", err, want)
			}
			var got *pgconn.PgError
			if !errors.As(err, &got) || got != pgErr {
				t.Error("the error of PostgreSQL is lost")
			}
		})
	}
	// A statement of the batch other than SET CONSTRAINTS gets no advice of
	// a constraint.
	other := "set the settings of the session to " + deferredWhat
	if err := deferredError(other, &pgconn.PgError{Code: "23505", Message: "m"}); err.Error() != other+": m" {
		t.Errorf("error of another statement = %s", err)
	}
}
