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

func TestNewRejectsInvalidConfig(t *testing.T) {
	ok := Config{Pool: &pgxpool.Pool{}, Channel: "ok", PollInterval: time.Second, OnChange: func(context.Context, *introspect.Catalog) {}}
	cases := []struct {
		edit func(*Config)
		want string
	}{
		{func(c *Config) { c.Channel = "bad$$chan" }, "invalid channel name"},
		{func(c *Config) { c.TriggerName = "no-dashes" }, "invalid trigger name"},
		{func(c *Config) { c.Channel = strings.Repeat("c", 64) }, "longer than 63 bytes"},
		{func(c *Config) { c.TriggerName = strings.Repeat("t", 57) }, "is too long"},
		{func(c *Config) { c.Channel = strings.Repeat("c", 51) }, "is too long"},
		{func(c *Config) { c.PollInterval = 0 }, "poll interval 0s must be positive"},
		{func(c *Config) { c.Pool = nil }, "Pool is required"},
		{func(c *Config) { c.OnChange = nil }, "OnChange is required"},
	}
	for _, tc := range cases {
		cfg := ok
		tc.edit(&cfg)
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, err = %v", tc.want, err)
		}
	}
	schemas := []string{"app"}
	cfg := ok
	cfg.Schemas = schemas
	w, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	schemas[0] = "changed"
	if w.cfg.TriggerName != "ok_watch" || w.cfg.Log == nil || w.cfg.InstallWait != DefaultInstallWait || w.cfg.Schemas[0] != "app" {
		t.Errorf("defaults = %+v", w.cfg)
	}
	if w, _ := New(ok); len(w.cfg.Schemas) != 1 || w.cfg.Schemas[0] != "public" {
		t.Errorf("default schemas = %v", w.cfg.Schemas)
	}
}

func mustNew(t *testing.T, cfg Config) *Watcher {
	t.Helper()
	w, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func testWatcher(cfg Config) *Watcher {
	return &Watcher{cfg: cfg, ready: make(chan struct{})}
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
	w := mustNew(t, Config{
		Pool: pool, Schemas: []string{"watch_test"}, Channel: "pdbcore_test_ddl",
		PollInterval: time.Second,
		OnChange:     func(_ context.Context, c *introspect.Catalog) { changes <- c },
	})
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

// TestWatchDDLDuringStartup runs DDL in a transaction that is open while Run
// installs the event trigger and commits only after the trigger exists. The
// change is neither notified nor visible to introspection until the commit,
// so Run must hold Ready until that transaction ends. Run with -count to
// shake out the race.
func TestWatchDDLDuringStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := watchTestPool(t, ctx)
	const schema = "watch_startup_test"
	const trigger = "pdbcore_startup_watch"
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	defer dropTriggers(pool, trigger)
	dropTriggers(pool, trigger)
	cat, err := introspect.Introspect(ctx, pool, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := cat.Hash()
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "CREATE TABLE "+schema+".probe (id int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}

	changes := make(chan *introspect.Catalog, 4)
	w := mustNew(t, Config{
		Pool: pool, Schemas: []string{schema}, Channel: "pdbcore_startup",
		PollInterval: time.Second, InitialHash: hash,
		OnChange: func(_ context.Context, c *introspect.Catalog) { changes <- c },
	})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	waitTriggerInstalled(t, ctx, pool, trigger+"_drop")
	select {
	case <-w.Ready():
		t.Fatal("Ready closed while a transaction open at trigger install was still running")
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
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
	w := mustNew(t, Config{
		Pool: pool, Schemas: []string{schema}, Channel: "pdbcore_poll",
		PollInterval: 100 * time.Millisecond, InitialHash: hash,
		OnChange: func(_ context.Context, c *introspect.Catalog) { changes <- c },
	})
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

func waitTriggerInstalled(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = $1)", name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event trigger %s never installed", name)
}
