package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

// keptForWrites is the one pinned setting that the writes of an apply keep:
// client_encoding is about the bytes between kortopi and the server, and a
// write takes its rows from temporary tables, in the types of the database
// already (ADR-0037).
const keptForWrites = "client_encoding"

// pinnedForWrites returns the settings that a write of an apply changes
// back to the values of the session, with their pinned values: every pinned
// setting but keptForWrites.
func pinnedForWrites() [][2]string {
	pinned := make([][2]string, 0, len(settings))
	for _, setting := range settings {
		if setting[0] != keptForWrites {
			pinned = append(pinned, setting)
		}
	}
	return pinned
}

// setSQL returns the statement that sets n settings until the end of the
// transaction, as SET LOCAL does: the parameters $1 and $2 are the name and
// the value of the first, and so on. It runs in the search path of the
// session too, so its function is named with its schema.
func setSQL(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "pg_catalog.set_config($" + strconv.Itoa(2*i+1) + ", $" + strconv.Itoa(2*i+2) + ", true)"
	}
	return "SELECT " + strings.Join(parts, ", ")
}

// setParams returns the parameters of setSQL for values, as text.
func setParams(values [][2]string) ([][]byte, []uint32) {
	params := make([][]byte, 0, 2*len(values))
	oids := make([]uint32, 0, 2*len(values))
	for _, v := range values {
		params = append(params, []byte(v[0]), []byte(v[1]))
		oids = append(oids, pgtype.TextOID, pgtype.TextOID)
	}
	return params, oids
}

// queueSettings adds to b the statement that sets values.
func queueSettings(b *pgconn.Batch, values [][2]string) {
	params, oids := setParams(values)
	b.ExecParams(setSQL(len(values)), params, oids, nil, nil)
}

// readSessionSettings reads, before the transaction of an apply begins, the
// values that the settings of its writes have in the session, and writes
// commitSQL, which the transaction runs to commit. The query runs in the
// search path of the session, so it names everything with its schema.
func (s *Session) readSessionSettings(ctx context.Context) error {
	pinned := pinnedForWrites()
	names := make([]string, len(pinned))
	for i, p := range pinned {
		names[i] = p[0]
	}
	var values []string
	err := s.conn.QueryRow(ctx, `SELECT pg_catalog.array_agg(pg_catalog.current_setting(n.name) ORDER BY n.i)
		FROM pg_catalog.unnest($1::pg_catalog.text[]) WITH ORDINALITY AS n(name, i)`, names).Scan(&values)
	if err != nil {
		return fmt.Errorf("read the settings of the session: %w", err)
	}
	if len(values) != len(names) {
		return fmt.Errorf("read the settings of the session: %d values for %d settings", len(values), len(names))
	}
	s.sessionSettings = make([][2]string, len(names))
	for i, name := range names {
		s.sessionSettings[i] = [2]string{name, values[i]}
	}
	s.commitSQL, err = commitSQL(s.sessionSettings)
	return err
}

// commitSQL returns the statements that commit an apply: they set the
// settings of the session again, so that the deferred constraints and
// triggers that the commit fires see what a commit of the application sees,
// and commit. Both go in one message of the simple protocol, as pgx sends a
// commit, so that a commit that gets no answer stays one request whose
// outcome is not known. The literals read the same in any session, and the
// settings are pinned when the commit comes anyway.
func commitSQL(values [][2]string) (string, error) {
	sets := make([]string, len(values))
	for i, v := range values {
		name, err := literal(plan.Value{Text: v[0]})
		if err != nil {
			return "", fmt.Errorf("the name of setting %q is not valid UTF-8", v[0])
		}
		value, err := literal(plan.Value{Text: v[1]})
		if err != nil {
			return "", fmt.Errorf("the value of setting %s is not valid UTF-8", v[0])
		}
		sets[i] = "pg_catalog.set_config(" + name + ", " + value + ", true)"
	}
	return "SELECT " + strings.Join(sets, ", ") + "; COMMIT", nil
}

// batchStatement is a statement of the batch of a write, with what an error
// of it says that the statement did, as in "pick the rows to insert into
// table app.t".
type batchStatement struct {
	what string
	sql  string
}

// execWrite runs write, a statement of an apply that writes rows of a
// table, with the settings that the session had before kortopi pinned them,
// and returns the command tags of the first statement of prepare, the pick,
// and of write (ADR-0037). The statements of prepare run
// first, in the pinned settings: they pick the rows that write writes, with
// the conditions of the plan, so that every value is compared as the plan
// compared it. The settings are pinned again after write, for what the apply
// reads next. All of it goes to the server in one round trip, and every
// setting is local to the transaction.
//
// On an error, failed says what the statement that failed did, and
// isWrite is true when it is write itself.
func (s *Session) execWrite(ctx context.Context, prepare []batchStatement, write batchStatement) (pick, wrote pgconn.CommandTag, failed string, isWrite bool, _ error) {
	b := &pgconn.Batch{}
	whats := make([]string, 0, len(prepare)+3)
	for _, p := range prepare {
		b.ExecParams(p.sql, nil, nil, nil, nil)
		whats = append(whats, p.what)
	}
	queueSettings(b, s.sessionSettings)
	b.ExecParams(write.sql, nil, nil, nil, nil)
	queueSettings(b, pinnedForWrites())
	whats = append(whats, "set the settings of the session to "+write.what, write.what,
		"pin the settings of the session again after the statement to "+write.what)
	results, err := s.tx.Conn().PgConn().ExecBatch(ctx, b).ReadAll()
	written := len(prepare) + 1
	if err == nil {
		return results[0].CommandTag, results[written].CommandTag, "", false, nil
	}
	i := min(failedStatement(results), len(whats)-1)
	return pgconn.CommandTag{}, pgconn.CommandTag{}, whats[i], i == written, err
}

// failedStatement returns the index of the statement of a batch that failed,
// from the results that the batch returned before its error: the first
// result with an error, or else the statement after the last result. A
// statement that returns rows has a result of its own when it fails, one
// that returns none does not.
func failedStatement(results []*pgconn.Result) int {
	for i, r := range results {
		if r.Err != nil {
			return i
		}
	}
	return len(results)
}
