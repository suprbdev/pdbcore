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

// Watcher triggers OnChange whenever the database schema drifts.
type Watcher struct {
	Pool         *pgxpool.Pool
	Schemas      []string
	Channel      string
	PollInterval time.Duration
	OnChange     func(cat *introspect.Catalog)
	Log          *slog.Logger
	// TriggerName names the event trigger; the notify function is
	// TriggerName + "_notify". Defaults to Channel + "_watch". Products
	// sharing a database must use distinct names or the last one to start
	// repoints the shared function at its own channel.
	TriggerName string
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
	return w.listen(ctx, conn.Conn())
}

// installTrigger creates (idempotently) a function + event trigger that
// NOTIFYs our channel at the end of every DDL command.
func (w *Watcher) installTrigger(ctx context.Context) error {
	fnName := w.TriggerName + "_notify"
	fn := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s() RETURNS event_trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_notify(%s, tg_tag);
		END $$;`, quoteIdent(fnName), quoteLit(w.Channel))
	if _, err := w.Pool.Exec(ctx, fn); err != nil {
		return err
	}
	_, err := w.Pool.Exec(ctx, fmt.Sprintf(`
		DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = %s) THEN
				CREATE EVENT TRIGGER %s ON ddl_command_end EXECUTE FUNCTION %s();
			END IF;
		END $$;`, quoteLit(w.TriggerName), quoteIdent(w.TriggerName), quoteIdent(fnName)))
	return err
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
	var lastHash string
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
			if lastHash != "" && hash != lastHash {
				w.Log.Info("watch: schema drift detected (poll)")
				w.OnChange(cat)
			}
			lastHash = hash
		}
	}
}

func (w *Watcher) reintrospect(ctx context.Context) {
	cat, err := introspect.Introspect(ctx, w.Pool, w.Schemas)
	if err != nil {
		w.Log.Error("watch: re-introspection failed", "err", err)
		return
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
