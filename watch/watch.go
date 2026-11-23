// Package watch re-introspects on DDL changes (dev mode). Preferred
// mechanism: a DDL event trigger NOTIFYing a channel that we LISTEN on.
// Fallback (no permission to create event triggers): poll the catalog hash.
package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/introspect"
)

// Watcher triggers OnChange whenever the introspected catalog of Schemas
// changes. The event trigger only notifies for DDL that can touch Schemas
// (objects in them, objects without a schema such as grants and
// extensions, and ALTERs that may move an object out of them); temp-schema
// objects never notify. A notification or poll whose catalog hash equals
// the last one applied does not call OnChange.
type Watcher struct {
	Pool         *pgxpool.Pool
	Schemas      []string
	Channel      string
	PollInterval time.Duration
	OnChange     func(cat *introspect.Catalog)
	Log          *slog.Logger
	// TriggerName names the ddl_command_end event trigger; the sql_drop
	// trigger is TriggerName + "_drop" and the notify function they share
	// is TriggerName + "_notify". Defaults to Channel + "_watch". Products
	// sharing a database must use distinct names or the last one to start
	// repoints the shared function at its own channel and schemas.
	TriggerName string
	// InitialHash is the [introspect.Catalog.Hash] of the catalog the
	// caller's current API was built from. When set, Run re-introspects
	// once as soon as it is listening, so a change made between the
	// caller's introspection and Run is not missed, and every later
	// comparison starts from it. When empty, the first DDL event always
	// calls OnChange and the first poll only records the hash.
	InitialHash string
	// InstallWait bounds how long Run waits, after installing the event
	// trigger, for transactions that were open at that moment to end before
	// the InitialHash re-check. Only used when InitialHash is set. Zero
	// means [DefaultInstallWait]. When it elapses Run logs a warning and
	// re-checks anyway; DDL in a still-open transaction that commits later
	// is then not reported.
	InstallWait time.Duration

	lastHash  string
	readyOnce sync.Once
	ready     chan struct{}
}

func (w *Watcher) readyChan() chan struct{} {
	w.readyOnce.Do(func() { w.ready = make(chan struct{}) })
	return w.ready
}

// DefaultInstallWait is the default [Watcher.InstallWait].
const DefaultInstallWait = 30 * time.Second

const installPollInterval = 50 * time.Millisecond

// Ready returns a channel that is closed once Run is listening for DDL
// notifications, or, when the event trigger cannot be installed, once the
// polling loop has started. Every change made after Ready is closed reaches
// OnChange, as does any change made since InitialHash was taken. The channel
// is never closed if Run returns an error before reaching that point. It may
// be called before Run.
//
// When InitialHash is set and the event trigger was installed, Ready is
// closed only after every transaction that was open at install time has
// ended and the catalog has been re-checked against InitialHash, because
// DDL run in such a transaction before the trigger existed is neither
// notified nor visible until it commits. A long-running transaction
// therefore delays Ready, up to [Watcher.InstallWait].
func (w *Watcher) Ready() <-chan struct{} {
	return w.readyChan()
}

// Run blocks until ctx is cancelled. It tries to install the event trigger;
// on permission failure it degrades to hash polling with a warning.
func (w *Watcher) Run(ctx context.Context) error {
	if w.Log == nil {
		w.Log = slog.Default()
	}
	if !validChannel(w.Channel) {
		return fmt.Errorf("watch: invalid channel name %q: only letters, digits and underscores are allowed", w.Channel)
	}
	if w.TriggerName == "" {
		w.TriggerName = w.Channel + "_watch"
	}
	if !validChannel(w.TriggerName) {
		return fmt.Errorf("watch: invalid trigger name %q: only letters, digits and underscores are allowed", w.TriggerName)
	}
	if len(w.Channel) > maxIdentLen {
		return fmt.Errorf("watch: channel name %q is longer than %d bytes", w.Channel, maxIdentLen)
	}
	if len(w.TriggerName)+len(longestSuffix) > maxIdentLen {
		return fmt.Errorf("watch: trigger name %q is too long: with the %q suffix it must fit in %d bytes", w.TriggerName, longestSuffix, maxIdentLen)
	}
	if len(w.Schemas) == 0 {
		w.Schemas = []string{"public"}
	}
	w.lastHash = w.InitialHash
	ready := w.readyChan()
	conn, err := w.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+quoteIdent(w.Channel)); err != nil {
		return err
	}
	if err := w.installTrigger(ctx); err != nil {
		w.Log.Warn("watch: cannot install DDL event trigger; falling back to polling",
			"err", err, "interval", w.PollInterval)
		return w.poll(ctx)
	}
	w.Log.Info("watch: DDL event trigger installed", "channel", w.Channel)
	if w.lastHash != "" {
		if err := w.waitInstallTxns(ctx); err != nil {
			return nil
		}
		w.reintrospect(ctx)
	}
	close(ready)
	return w.listen(ctx, conn.Conn())
}

// waitInstallTxns blocks until every transaction holding an xid below the
// snapshot's xmax taken now, after the trigger commit, has ended, or until
// InstallWait elapses. A transaction that ran DDL before the trigger became
// visible has an xid in that range; one that runs DDL later fires the
// trigger. It returns the context error if ctx is cancelled.
func (w *Watcher) waitInstallTxns(ctx context.Context) error {
	var xmax string
	if err := w.Pool.QueryRow(ctx, `SELECT pg_snapshot_xmax(pg_current_snapshot())::text`).Scan(&xmax); err != nil {
		w.Log.Warn("watch: cannot snapshot open transactions; re-checking immediately", "err", err)
		return nil
	}
	wait := w.InstallWait
	if wait <= 0 {
		wait = DefaultInstallWait
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(installPollInterval)
	defer tick.Stop()
	for {
		var done bool
		err := w.Pool.QueryRow(ctx, `SELECT pg_snapshot_xmin(pg_current_snapshot()) >= $1::xid8`, xmax).Scan(&done)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.Log.Warn("watch: cannot poll open transactions; re-checking immediately", "err", err)
			return nil
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			w.Log.Warn("watch: transactions open at trigger install still running; re-checking anyway", "wait", wait)
			return nil
		case <-tick.C:
		}
	}
}

// installTrigger creates or replaces the notify function and creates the
// ddl_command_end and sql_drop event triggers when missing. Installs from
// older versions (one unfiltered trigger) are upgraded in place.
func (w *Watcher) installTrigger(ctx context.Context) error {
	fnName := w.TriggerName + "_notify"
	fn := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s() RETURNS event_trigger
		LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS %s;`, quoteIdent(fnName), dollarQuote(notifyBody(w.Channel, w.Schemas)))
	if _, err := w.Pool.Exec(ctx, fn); err != nil {
		return err
	}
	for _, t := range []struct{ name, event string }{
		{w.TriggerName, "ddl_command_end"},
		{w.TriggerName + "_drop", "sql_drop"},
	} {
		_, err := w.Pool.Exec(ctx, fmt.Sprintf(`
		DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = %s) THEN
				CREATE EVENT TRIGGER %s ON %s EXECUTE FUNCTION %s();
			END IF;
		END $$;`, quoteLit(t.name), quoteIdent(t.name), t.event, quoteIdent(fnName)))
		if err != nil {
			return err
		}
	}
	return nil
}

// notifyBody is the plpgsql body of the notify function. It notifies only
// when the command can change the catalog of schemas:
//
//   - ddl_command_end: an object in schemas; a command without a schema
//     (GRANT, CREATE EXTENSION, ALTER SCHEMA, ...); or an ALTER of a
//     relation, type, domain or routine in any non-temp schema, because
//     ALTER ... SET SCHEMA reports only the new schema and a renamed type
//     can be referenced by a watched column.
//   - sql_drop: a non-temp object in schemas, a schema-less dependent
//     (default, policy, trigger, rule) of a table in schemas, a dropped
//     watched schema, or any other schema-less object (extensions, ...).
func notifyBody(channel string, schemas []string) string {
	lits := make([]string, len(schemas))
	for i, s := range schemas {
		lits[i] = quoteLitSafe(s)
	}
	watched := "ARRAY[" + strings.Join(lits, ", ") + "]::text[]"
	return fmt.Sprintf(`
BEGIN
	IF tg_event = 'sql_drop' THEN
		IF NOT EXISTS (
			SELECT 1 FROM pg_event_trigger_dropped_objects() AS o
			WHERE NOT o.is_temporary
			  AND (o.schema_name = ANY (%[1]s)
			       OR (o.schema_name IS NULL
			           AND (o.address_names[1] = ANY (%[1]s)
			                OR o.object_type NOT IN ('default value', 'policy', 'trigger', 'rule', 'schema'))))) THEN
			RETURN;
		END IF;
	ELSIF NOT EXISTS (
		SELECT 1 FROM pg_event_trigger_ddl_commands() AS c
		WHERE c.schema_name IS NULL
		   OR c.schema_name = ANY (%[1]s)
		   OR (c.command_tag LIKE 'ALTER %%'
		       AND c.object_type IN ('table', 'view', 'materialized view', 'foreign table', 'type', 'domain', 'function', 'procedure')
		       AND c.schema_name NOT LIKE E'pg\\_temp%%'
		       AND c.schema_name NOT LIKE E'pg\\_toast%%')) THEN
		RETURN;
	END IF;
	PERFORM pg_notify(%[2]s, tg_tag);
END`, watched, quoteLit(channel))
}

func (w *Watcher) listen(ctx context.Context, conn *pgx.Conn) error {
	for {
		_, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		// Debounce bursts of DDL (e.g. a migration) before re-introspecting.
		time.Sleep(250 * time.Millisecond)
		drainNotifications(ctx, conn)
		w.reintrospect(ctx)
	}
}

func drainNotifications(ctx context.Context, conn *pgx.Conn) {
	for {
		drainCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		_, err := conn.WaitForNotification(drainCtx)
		cancel()
		if err != nil {
			return
		}
	}
}

func (w *Watcher) poll(ctx context.Context) error {
	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()
	close(w.readyChan())
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cat, err := introspect.Introspect(ctx, w.Pool, w.Schemas)
			if err != nil {
				w.Log.Warn("watch: poll introspection failed", "err", err)
				continue
			}
			hash, err := cat.Hash()
			if err != nil {
				continue
			}
			if w.lastHash == "" {
				w.lastHash = hash
				continue
			}
			if hash != w.lastHash {
				w.lastHash = hash
				w.Log.Info("watch: schema drift detected (poll)")
				w.OnChange(cat)
			}
		}
	}
}

func (w *Watcher) reintrospect(ctx context.Context) {
	cat, err := introspect.Introspect(ctx, w.Pool, w.Schemas)
	if err != nil {
		w.Log.Error("watch: re-introspection failed", "err", err)
		return
	}
	if hash, err := cat.Hash(); err == nil {
		if hash == w.lastHash {
			w.Log.Debug("watch: DDL left the catalog unchanged; not rebuilding")
			return
		}
		w.lastHash = hash
	}
	w.Log.Info("watch: schema changed, rebuilding")
	w.OnChange(cat)
}

// maxIdentLen is PostgreSQL's identifier limit (NAMEDATALEN - 1); longer
// names are silently truncated, which could make two triggers collide.
const maxIdentLen = 63

// longestSuffix is the longest suffix installTrigger appends to TriggerName.
const longestSuffix = "_notify"

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// quoteLitSafe quotes s as a string literal that reads the same whatever
// standard_conforming_strings is set to: an escape string (E'...') when s
// contains a backslash.
func quoteLitSafe(s string) string {
	if !strings.Contains(s, `\`) {
		return quoteLit(s)
	}
	return "E'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
}

// dollarQuote wraps body in a dollar quote whose tag does not occur in it.
func dollarQuote(body string) string {
	tag := "$watch$"
	for i := 0; strings.Contains(body, tag); i++ {
		tag = fmt.Sprintf("$watch%d$", i)
	}
	return tag + body + tag
}

// validChannel restricts the NOTIFY channel to [a-zA-Z0-9_]. Escaping alone
// is not enough: the channel literal is embedded in a $$-quoted plpgsql body,
// so a value containing "$$" would terminate the dollar quote regardless of
// quote doubling.
func validChannel(s string) bool {
	for _, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return s != ""
}
