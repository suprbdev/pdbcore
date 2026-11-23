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

	lastHash string
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
	if len(w.Schemas) == 0 {
		w.Schemas = []string{"public"}
	}
	w.lastHash = w.InitialHash
	// LISTEN before installing the trigger so no notification fired after
	// the install can be lost; the "installed" log line therefore also means
	// "listening".
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
		w.reintrospect(ctx)
	}
	return w.listen(ctx, conn.Conn())
}

// installTrigger creates or replaces the notify function and creates the
// ddl_command_end and sql_drop event triggers when missing. Installs from
// older versions (one unfiltered trigger) are upgraded in place.
func (w *Watcher) installTrigger(ctx context.Context) error {
	fnName := w.TriggerName + "_notify"
	fn := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s() RETURNS event_trigger
		LANGUAGE plpgsql AS %s;`, quoteIdent(fnName), dollarQuote(notifyBody(w.Channel, w.Schemas)))
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
		       AND c.schema_name NOT LIKE 'pg\_temp%%'
		       AND c.schema_name NOT LIKE 'pg\_toast%%')) THEN
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
