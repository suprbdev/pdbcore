package watch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	w = &Watcher{Channel: strings.Repeat("c", 64)}
	if err := w.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "longer than 63 bytes") {
		t.Fatalf("err = %v", err)
	}
	w = &Watcher{Channel: "ok", TriggerName: strings.Repeat("t", 57)}
	if err := w.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "is too long") {
		t.Fatalf("err = %v", err)
	}
	w = &Watcher{Channel: strings.Repeat("c", 51)}
	if err := w.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "is too long") {
		t.Fatalf("default trigger name from a long channel: err = %v", err)
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
	defer dropTriggers(pool, "pdbcore_test_ddl_watch")

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

func watchTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func awaitStop(t *testing.T, stop context.CancelFunc, done <-chan error) {
	t.Helper()
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

// TestWatchDDLDuringStartup issues DDL immediately after Run starts, with no
// wait for the trigger or LISTEN, and requires OnChange to see it. Run with
// -count to shake out the race.
func TestWatchDDLDuringStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := watchTestPool(t, ctx)
	const schema = "watch_startup_test"
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	defer dropTriggers(pool, "pdbcore_startup_watch")
	cat, err := introspect.Introspect(ctx, pool, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := cat.Hash()
	if err != nil {
		t.Fatal(err)
	}

	changes := make(chan *introspect.Catalog, 4)
	w := &Watcher{
		Pool: pool, Schemas: []string{schema}, Channel: "pdbcore_startup",
		PollInterval: time.Second, InitialHash: hash,
		OnChange: func(c *introspect.Catalog) { changes <- c },
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".probe (id int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	waitProbe(t, changes, schema)
	select {
	case <-w.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("Ready not closed")
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".probe2 (id int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	waitTable(t, changes, schema, "probe2")
	awaitStop(t, stop, done)
}

// TestWatchReadyPollFallback runs as a role that cannot create event
// triggers, so Run polls, and checks Ready closes and later DDL is detected.
func TestWatchReadyPollFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin := watchTestPool(t, ctx)
	const schema = "watch_poll_test"
	const role = "watch_poll_test_role"
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	dropRole := "DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '" + role + "') THEN DROP OWNED BY " + role + "; DROP ROLE " + role + "; END IF; END $$"
	if _, err := admin.Exec(ctx, dropRole); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" NOSUPERUSER"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), dropRole)
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + schema + " TO " + role,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA " + schema + " GRANT ALL ON TABLES TO " + role,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv("PDBCORE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+role)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	cat, err := introspect.Introspect(ctx, admin, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := cat.Hash()
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan *introspect.Catalog, 4)
	w := &Watcher{
		Pool: pool, Schemas: []string{schema}, Channel: "pdbcore_poll",
		PollInterval: 100 * time.Millisecond, InitialHash: hash,
		OnChange: func(c *introspect.Catalog) { changes <- c },
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	select {
	case <-w.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("Ready not closed in poll fallback")
	}
	if _, err := admin.Exec(ctx, "CREATE TABLE "+schema+".probe (id int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	waitProbe(t, changes, schema)
	awaitStop(t, stop, done)
}

func waitProbe(t *testing.T, changes <-chan *introspect.Catalog, schema string) {
	t.Helper()
	waitTable(t, changes, schema, "probe")
}

func waitTable(t *testing.T, changes <-chan *introspect.Catalog, schema, table string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case cat := <-changes:
			if cat.Table(schema, table) != nil {
				return
			}
		case <-deadline:
			t.Fatalf("OnChange never reported %s.%s", schema, table)
		}
	}
}
