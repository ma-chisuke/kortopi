---
status: accepted
date: 2026-11-22
---

# ADR-0043: Cancel the statement of an interrupted run on the server, and give up the wait for a commit only on a second interrupt or a dead connection

## Context and Problem Statement

ADR-0026 rolls an apply back on an interrupt during the checks or the writes, and says that nothing was changed.
Once the commit has started, an interrupt does not stop it, so that its outcome is known, and a commit that gets no answer is an apply whose outcome is not known.
A plan and an export roll back on an interrupt too.

kortopi turned an interrupt, SIGINT or SIGTERM, into the cancellation of a context, and a second interrupt ended the process at once.
On a cancelled context, the driver, pgx, sets a deadline on the socket by default, stops reading, and closes the connection in the background.
Its cancel request goes out from that background work, which the exit of kortopi cuts off.
The server therefore goes on with the statement, and holds its locks, the lock that serializes applies among them, until the statement ends by itself: 88 and 132 seconds after kortopi had exited, in an update of a million rows, and until another session released a row that the update waited for.
kortopi had meanwhile said that nothing was changed, and the next apply waited for the lock of kortopi, or stopped with an error that blamed "the other apply", which did not exist (issue 42).

The commit has the opposite problem (issue 44).
When the network between kortopi and the server is cut while the commit waits for its answer, kortopi waits with no output and with the keepalive of the runtime, which probes after 15 seconds, every 15 seconds, 9 times, so 150 seconds pass before it notices.
A first interrupt was put off without a word, and a second one killed kortopi with no message, although the change had been committed.

What does an interrupted run do on the server, what does it say, and how does the wait for a commit end?

## Decision Drivers

- After an interrupt, the work of kortopi on the server ends, and its locks go, before kortopi exits, or kortopi says that it may not have ended and how to end it.
- kortopi never says that nothing was changed when that may be false, and says what it knows in each case.
- An interrupt alone never makes the outcome of a commit unknown, and kortopi never dies from an interrupt without a message.
- A connection that no longer reaches the server ends a wait in bounded time; a slow server that still answers is waited for.
- The wait after an interrupt is short, because a job runner that cancels a job usually kills the process some seconds after its first signal.
- The parts that the driver offers are used, and no dependency is added.

## Considered Options

For the statement that runs at an interrupt:

- The cancel request of the protocol, which the driver sends, and a rollback, within 5 seconds of the interrupt
- Close the connection, as before
- `pg_cancel_backend` or `pg_terminate_backend` from a second connection
- Leave it to `client_connection_check_interval` on the server

For the wait for the answer of a commit:

- TCP keepalive that notices a silent server in about 30 seconds, and a second interrupt that gives up the wait, with no cancel request
- A time limit of the commit, as a flag
- A cancel request of the commit on the second interrupt
- `TCP_USER_TIMEOUT`

## Decision Outcome

Chosen options: "The cancel request of the protocol, which the driver sends, and a rollback, within 5 seconds of the interrupt", because the server then ends the statement at its next check for an interrupt, and kortopi can know that it did; and "TCP keepalive that notices a silent server in about 30 seconds, and a second interrupt that gives up the wait, with no cancel request", because a commit then waits as long as the server answers, and the person or the job that runs kortopi decides when to stop waiting, with a message that tells the truth about the outcome.

The rules are as follows.

An interrupt before the commit:

- The first interrupt cancels the context of the run. The session then sends the cancel request of the protocol at once, on a new connection to the address of its own, with the process ID and the secret key that the server gave the session, and gives the read of the answer a deadline 5 seconds away. This is the handler `CancelRequestContextWatcherHandler` of pgx, without delay.
- The server ends the statement at its next check for an interrupt, which comes within milliseconds in a scan, a sort, a wait for a lock, or `pg_sleep`, and answers with an error. kortopi then rolls the transaction back, and closes the connection. The rollback releases every lock of the transaction, the lock of kortopi among them, and the server process ends with the connection.
- The bound is 5 seconds after the interrupt, for the end of the statement and the rollback together. A server that has not answered by then is busy elsewhere, or out of reach, and a job runner that waits a few seconds after its first signal is not outwaited.
- When the server answered and the rollback came back, the messages are those of ADR-0026: `kortopi: interrupted: the apply was stopped` and `kortopi: nothing was changed`, and the lines of a plan and of an export as before.
- When it did not, kortopi says so on standard error before the last line, and exits with status 1:

  ```text
  kortopi: interrupted: the apply was stopped
  kortopi: the server did not confirm within 5s that the session of the apply ended: its statement may still run on the server and hold its locks until the server notices that kortopi has gone, although its transaction can no longer commit: find the session in pg_stat_activity, where application_name is "kortopi" and pid is 386, and end it with pg_cancel_backend(386), or with pg_terminate_backend(386) if it does not end
  kortopi: nothing was changed
  ```

- "nothing was changed" is true there: kortopi never sent `COMMIT`, and its connection is closed, so the transaction can no longer commit, and the server rolls it back when the session ends. What is not known is when that is, so the line before it says so.
- The process ID is the one that `pg_backend_pid()` returns in the first query of each transaction of the session, not the one of the start-up of the connection, which a connection pooler in transaction mode does not take from the server. A transaction that has not read it yet is named by its `application_name` alone. A plan and an export say the same with "plan" or "export" in place of "apply", and a plan says that the database is as it was, as before.
- A statement that would start after the interrupt is not sent, and the rollback is left to the close of the session, which has a context of its own: pgx breaks the connection when a `BEGIN` or a rollback fails without an answer, which would turn a session that ended into one that kortopi cannot confirm.
- The same holds for the wait for another apply, which is a statement in a transaction of its own.

The commit:

- From the start of the commit, no cancel request leaves kortopi. A cancel during the wait for a synchronous standby makes the server report the commit as done locally, and a cancel during a deferred trigger rolls it back: the outcome would depend on what the commit was doing, which the person who interrupts cannot see. The session ends a wait of the commit without a cancel request, and its dialer refuses the connection of a cancel request, which pgx sends when it abandons a connection.
- A first interrupt after the commit has started is put off, and kortopi says so at once on standard error:

  ```text
  kortopi: the commit has started, so the interrupt does not stop it: waiting for the answer of the database, so that the outcome is known: interrupt again to give up the wait
  ```

- The second interrupt ends the wait at once: the session sets a deadline that has passed on its connection, and kortopi prints the message of the commit without an answer, with its cause, and exits with status 1:

  ```text
  kortopi: commit the apply: the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database (cause: a second interrupt gave up the wait for the answer)
  ```

- The server finishes the commit after that, or rolls it back if a deferred constraint fails, so `kortopi plan` shows which.
- kortopi never dies from the first two interrupts. After the second, it no longer catches the signals, so a third one ends the process at once, by the default action of the signal, as a way out of a run that does not end for another reason, such as a standard output that nobody reads.
- A second interrupt before the commit ends the bounded wait of the rollback at once too, with `kortopi stopped waiting for the server to end the session of the apply` in place of the first part of the message above.
- The second interrupt means "stop waiting", not "do not cancel", so it does not keep the cancel request from going out, also when it comes right after the first: pgx sends one in the background as it gives the connection up, and kortopi waits up to 1 second for that before it exits, since the end of the process would cut it off. A server within reach takes it in milliseconds. Once the commit has started, no cancel request leaves kortopi, as above.

The keepalive:

- Every TCP connection of a session probes the server after 10 seconds without traffic, every 5 seconds, and drops after 4 probes without an answer: a server that no longer answers is noticed after 30 seconds of silence, in every phase, and a commit then ends with the message of the unknown outcome. The runtime would take 150 seconds, and a system that kortopi did not configure two hours.
- A server that answers its probes is waited for, however long the commit takes, as for a synchronous standby that is slow: the commit is in progress, and the second interrupt is the way out.
- The probes cost a few bytes every 5 seconds on an idle connection, and a cut that lasts less than 30 seconds does not end the run.
- The tuning is best effort, as in the dialer of Go: a value that the system refuses stays as the system has it, the keepalive is at least turned on, and the connection is kept, without a word. Windows before 10 version 1709 refuses to set the count of probes, and Linux and macOS refuse an idle time of millions of seconds.
- libpq reads the settings `keepalives`, `keepalives_idle`, `keepalives_interval`, and `keepalives_count` from a connection string or a service file, and has no environment variables for them. pgx reads them from a service file too, but does not know them, and would pass them to the server as settings of the session, which refuses them. kortopi takes them out and applies them as libpq does: `keepalives` turns the keepalive off when it is 0, and on otherwise; 0 for one of the others keeps the value of the system, and so does a negative value, which libpq takes for 0; a value that is not a whole number is an error. The value of the system is kept for real, because kortopi dials as the default dialer of pgx does but with the keepalive of Go off, which would otherwise set 15 seconds, 15 seconds, and 9 probes on every socket first. `tcp_user_timeout` stays what the driver makes of it, a setting of the server for its side of the connection.
- The keepalive probes only a connection without data that waits for its acknowledgement. A cut at the moment that kortopi sends `COMMIT`, before the server acknowledged it, leaves the wait to the retransmissions of the system, which can last many minutes on Linux. `TCP_USER_TIMEOUT` would bound them, but the package `net` of Go does not set it, and it exists on Linux only.
- Measured by hand once, as the tests cannot cut a network: the binary for Linux in a container, against PostgreSQL 18 in another, on a Docker network of their own, with the commit of an apply waiting for a synchronous standby that does not exist; after `docker network disconnect` of the client, the server was told to end the wait and committed, and kortopi printed the message of the unknown outcome, with the cause `unexpected EOF`, and exited with status 1, 30.0 seconds after the cut. By the settings of the keepalive of the runtime, the same run would have waited about 150 seconds; that was not measured.

Known limits, which kortopi accepts:

- A cancel request can come after its statement ended, and land on the next one, a `BEGIN` or the `ROLLBACK` of the close. pgx waits 100 milliseconds after each cancel request against this, and kortopi sends no statement after the interrupt but the rollback; when it happens all the same, kortopi cannot confirm the end of the session, and says that it may still run, although it has ended. The message says "may".
- When both interrupts come in the instant before `COMMIT` is written to the connection, the commit never reaches the server, but kortopi says that its outcome is not known: it cannot tell this from a commit that went out, and errs on the side that tells the user to look.

Through a connection pooler in transaction mode:

- A cancel request goes to the address of the connection, so to the pooler, which forwards it to the server connection that serves the client at that moment. No test of this repository runs through a pooler, as for ADR-0026.

This refines these rules of ADR-0026:

- "On any error, on an interrupt during the checks or the writes, and when the session closes without a commit, the transaction is rolled back, and the last line on standard error says that nothing was changed." On an interrupt, kortopi first asks the server to cancel the statement, and says before the last line when the server did not confirm the end of the session within 5 seconds.
- "When the answer of the commit does not arrive, because the connection broke, a timeout passed, or the server ended the session, the apply cannot know whether the changes were made." The keepalive of kortopi is such a timeout, and so is a second interrupt, whose message says so.
- "Once the commit has started, an interrupt does not stop it, so that its outcome is known." kortopi says so when the interrupt comes, and the second interrupt gives up the wait, not the commit.

### Consequences

- Good, because an interrupted run ends its work on the server within milliseconds in the common case, with its locks, so the next apply does not wait for a session that no longer serves anyone.
- Good, because "nothing was changed" stays true in every case where kortopi says it, and when the server did not confirm, kortopi says what may still run and how to end it, with the process ID that `pg_stat_activity` shows.
- Good, because an interrupt during the commit is answered at once, and a second one ends kortopi with the message of the unknown outcome instead of a death without a word.
- Good, because a cut network ends every wait of kortopi after about 30 seconds of silence, also outside the commit.
- Bad, because an interrupt can take up to 5 seconds to end when the server does not answer, where kortopi used to exit at once.
- Bad, because a network that is cut for longer than 30 seconds and then comes back ends the run with an unknown outcome, where the runtime would have waited 150 seconds and might have got the answer.
- Bad, because a cut at the very moment of the commit is left to the retransmissions of the system, which can last many minutes.
- Bad, because the cancel request needs a second connection to the server: where kortopi cannot open one, the run waits for the bound and says that the session may still run.

### Confirmation

- Unit tests in `cmd/kortopi` confirm the signals and the messages, with a fake session: `TestWatchInterrupts`, `TestInterruptedSessionNotEnded`, `TestCommitInterrupted`, and `TestRunApplyInterruptedUnknownOutcome`.
- Unit tests in `internal/postgres` confirm the keepalive, its settings of libpq, and a service file that holds them (`TestKeepAliveConfig`, `TestKeepAliveFromTheServiceFile`, `TestDialKeepAlive`, which reads the options of the socket), a dial refused during the commit, the message of a session that may still run (`TestNotEnded`), and the cause of a commit that was given up (`TestCommitGivenUp`).
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, which read `pg_stat_activity` and `pg_locks` through a second connection:
  - In `internal/postgres`, `TestInterruptEndsTheStatementOnTheServer`: an interrupt during a long update, during a wait for a row lock, and during the wait for another apply; the session confirms its end, its process holds no lock and goes, and no row changed. `TestInterruptWithoutAnAnswer`: a trigger that catches the cancel and sleeps again; the session stops waiting after its bound, or at the second interrupt, and its error names the process that still runs. `TestInterruptDuringTheCommit`: a commit that waits for an advisory lock in a deferred trigger is not stopped by the first interrupt; the second ends the wait with the unknown outcome, the process still waits on the server, and the commit completes once the lock is free. `TestKeepAliveOfTheSession`: the socket of a session has the keepalive of kortopi, or of the settings of libpq, which do not reach the server. `TestSecondInterruptStillCancels`: two interrupts back to back during a wait for a row lock; the cancel request goes out before Close returns, and the statement ends on the server although the row is still held.
  - In `cmd/kortopi`, `TestInterruptedApplyCommand` and `TestInterruptedCommitCommand` run the command in the same three cases and with the two messages of the commit.
- No test cuts the network, and none runs through a connection pooler.

## Pros and Cons of the Options

### The cancel request of the protocol, which the driver sends, and a rollback, within 5 seconds of the interrupt

- Good, because it is how `psql` and libpq cancel a query, it reaches the server process of the session directly, and the session stays usable for the rollback.
- Good, because pgx offers it as a handler of a cancelled context.
- Bad, because it opens a short second connection, and a statement that catches the error of the cancel goes on.

### Close the connection, as before

- Good, because it is immediate.
- Bad, because the server notices the closed connection only when the statement ends and it tries to answer, which can take minutes, and holds the locks until then.

### `pg_cancel_backend` or `pg_terminate_backend` from a second connection

- Good, because it can end a statement that catches the error of a cancel.
- Bad, because it needs a full second session: authentication, a free connection slot, and a privilege over the other session, and through a connection pooler in transaction mode it needs the process ID of the server that the transaction runs on.

### Leave it to `client_connection_check_interval` on the server

- Good, because the server then ends a statement soon after its client has gone, whatever the client does.
- Bad, because it is a setting of the server or of the role, which kortopi does not control, and it acts only on a connection that is closed, not on a client that waits.

### TCP keepalive that notices a silent server in about 30 seconds, and a second interrupt that gives up the wait, with no cancel request

- Good, because a commit that is slow but in progress is never given up by kortopi itself.
- Good, because the outcome of the commit stays in the hands of the server, and the message says that it is not known.
- Bad, because 30 seconds is a guess between a network that hiccups and a deploy that hangs.

### A time limit of the commit, as a flag

- Good, because a script could bound the whole run.
- Bad, because no number suits a commit that waits for a standby, and the limit would turn a slow commit into an unknown outcome on its own.

### A cancel request of the commit on the second interrupt

- Good, because the server might then answer, and the outcome would be known.
- Bad, because the cancel changes the outcome: it rolls back a commit in a deferred trigger, and turns a commit that waits for a standby into one that is done locally.

### `TCP_USER_TIMEOUT`

- Good, because it bounds a connection with data that waits for its acknowledgement too.
- Bad, because the package `net` of Go does not set it, and it exists on Linux only.

## More Information

- [Canceling requests in progress](https://www.postgresql.org/docs/current/protocol-flow.html#PROTOCOL-FLOW-CANCELING-REQUESTS), in the protocol of PostgreSQL
- [`client_connection_check_interval`](https://www.postgresql.org/docs/current/runtime-config-connection.html#GUC-CLIENT-CONNECTION-CHECK-INTERVAL), and the keepalive settings of the server, `tcp_keepalives_idle` and the others
- [The parameters of a connection of libpq](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-PARAMKEYWORDS), for `keepalives` and the others
- The reference for users: [docs/reference/apply.md](../reference/apply.md#interrupt-an-apply)
