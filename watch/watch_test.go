package watch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/introspect"
)

func TestRunRejectsInvalidNames(t *testing.T) {
	w := &Watcher{Channel: "bad$$chan"}
	if err := w.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid channel name") {
		t.Fatalf("err = %v", err)
	}
	w = &Watcher{Channel: "ok", TriggerName: "no-dashes"}
	if err := w.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid trigger name") {
		t.Fatalf("err = %v", err)
	}
}

func TestQuoting(t *testing.T) {
	if quoteIdent(`a"b`) != `"a""b"` || quoteLit(`it's`) != `'it''s'` {
		t.Fatal("quoting")
	}
	for s, want := range map[string]bool{"pdbq_ddl": true, "A1_": true, "": false, "x y": false, "x$$": false} {
		if validChannel(s) != want {
			t.Errorf("validChannel(%q) = %v", s, !want)
		}
	}
}

// TestWatchEndToEnd installs the event trigger against the fixture database
// and checks a DDL statement reaches OnChange. Uses its own schema so the
// probe never leaks into the drift gate's introspection of public.
func TestWatchEndToEnd(t *testing.T) {
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS watch_test"); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS watch_test CASCADE")

	changes := make(chan *introspect.Catalog, 4)
	w := &Watcher{
		Pool: pool, Schemas: []string{"watch_test"}, Channel: "pdbcore_test_ddl",
		PollInterval: time.Second,
		OnChange:     func(c *introspect.Catalog) { changes <- c },
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	// Give LISTEN a moment to be registered before firing DDL.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM pg_event_trigger WHERE evtname = 'pdbcore_test_ddl_watch'").Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event trigger was not installed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := pool.Exec(ctx, "CREATE TABLE watch_test.probe (id int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	select {
	case cat := <-changes:
		if cat.Table("watch_test", "probe") == nil {
			t.Fatalf("re-introspected catalog lacks the probe table: %+v", cat.Tables)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnChange not called after DDL")
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
