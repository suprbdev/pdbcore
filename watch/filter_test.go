package watch

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/introspect"
)

func TestQuoteLitSafeAndDollarQuote(t *testing.T) {
	cases := map[string]string{
		`plain`:   `'plain'`,
		`it's`:    `'it''s'`,
		`back\sl`: `E'back\\sl'`,
		`b\'q`:    `E'b\\''q'`,
	}
	for in, want := range cases {
		if got := quoteLitSafe(in); got != want {
			t.Errorf("quoteLitSafe(%q) = %s, want %s", in, got, want)
		}
	}
	if got := dollarQuote("x"); got != "$watch$x$watch$" {
		t.Fatalf("dollarQuote = %s", got)
	}
	if got := dollarQuote("a $watch$ b $watch0$"); got != "$watch1$a $watch$ b $watch0$$watch1$" {
		t.Fatalf("dollarQuote with colliding tags = %s", got)
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PDBCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBCORE_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// dropTriggers removes everything installTrigger creates for name.
func dropTriggers(pool *pgxpool.Pool, name string) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, "DROP EVENT TRIGGER IF EXISTS "+quoteIdent(name))
	_, _ = pool.Exec(ctx, "DROP EVENT TRIGGER IF EXISTS "+quoteIdent(name+"_drop"))
	_, _ = pool.Exec(ctx, "DROP FUNCTION IF EXISTS "+quoteIdent(name+"_notify")+"()")
}

// notifyProbe runs DDL on one dedicated connection and reports whether it
// made the trigger notify. Only notifications from that connection's
// backend count, so DDL from tests running in parallel cannot interfere,
// and a marker notification sent after each statement bounds the wait.
type notifyProbe struct {
	t        *testing.T
	channel  string
	ddl      *pgx.Conn
	listener *pgx.Conn
	pid      uint32
}

func newNotifyProbe(t *testing.T, pool *pgxpool.Pool, channel string) *notifyProbe {
	ctx := context.Background()
	acquire := func() *pgx.Conn {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Release)
		return c.Conn()
	}
	p := &notifyProbe{t: t, channel: channel, ddl: acquire(), listener: acquire()}
	p.pid = p.ddl.PgConn().PID()
	if _, err := p.listener.Exec(ctx, "LISTEN "+quoteIdent(channel)); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *notifyProbe) notifies(sql string) bool {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.ddl.Exec(ctx, sql); err != nil {
		p.t.Fatalf("%s: %v", sql, err)
	}
	if _, err := p.ddl.Exec(ctx, "SELECT pg_notify($1, 'marker')", p.channel); err != nil {
		p.t.Fatal(err)
	}
	got := false
	for {
		n, err := p.listener.WaitForNotification(ctx)
		if err != nil {
			p.t.Fatalf("waiting for the marker after %s: %v", sql, err)
		}
		if n.PID != p.pid {
			continue
		}
		if n.Payload == "marker" {
			return got
		}
		got = true
	}
}

func TestNotifyFilter(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const schema, other, name = "watch_filter", "watch_filter_other", "pdbcore_filter_test"
	cleanup := func() {
		dropTriggers(pool, name)
		_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+", "+other+" CASCADE")
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, s := range []string{"CREATE SCHEMA " + schema, "CREATE SCHEMA " + other} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	w := &Watcher{Pool: pool, Schemas: []string{schema}, Channel: name, TriggerName: name}
	if err := w.installTrigger(ctx); err != nil {
		t.Fatal(err)
	}
	p := newNotifyProbe(t, pool, name)

	steps := []struct {
		sql  string
		want bool
	}{
		{"CREATE TEMP TABLE tt (id serial PRIMARY KEY, x int DEFAULT 3)", false},
		{"CREATE INDEX ON tt (x)", false},
		{"ALTER TABLE tt ADD COLUMN y int", false},
		{"DROP TABLE tt", false},
		{"CREATE TABLE " + other + ".x (id serial PRIMARY KEY, a int)", false},
		{"CREATE TABLE " + other + ".y (id int GENERATED ALWAYS AS IDENTITY)", false},
		{"CREATE INDEX ON " + other + ".x (a)", false},
		{"CREATE TYPE " + other + ".mood AS ENUM ('a')", false},
		{"CREATE FUNCTION " + other + ".gen() RETURNS int LANGUAGE sql AS 'SELECT 1'", false},
		{"DROP TABLE " + other + ".y", false},

		{"CREATE TABLE " + schema + ".t (id int DEFAULT " + other + ".gen(), a int)", true},
		{"CREATE INDEX ON " + schema + ".t (a)", true},
		{"COMMENT ON TABLE " + schema + ".t IS 'hello'", true},
		{"ALTER TABLE " + schema + ".t ADD COLUMN b int", true},
		{"ALTER TABLE " + schema + ".t DROP COLUMN b", true},
		{"GRANT SELECT ON " + schema + ".t TO PUBLIC", true},
		{"ALTER TYPE " + other + ".mood ADD VALUE 'b'", true},
		{"ALTER TABLE " + other + ".x SET SCHEMA " + schema, true},
		{"ALTER TABLE " + schema + ".x SET SCHEMA " + other, true},
		{"DROP FUNCTION " + other + ".gen() CASCADE", true},
		{"DROP TABLE " + schema + ".t", true},
	}
	for _, s := range steps {
		if got := p.notifies(s.sql); got != s.want {
			t.Errorf("%s: notified=%v, want %v", s.sql, got, s.want)
		}
	}
}

// TestNotifyFilterHostileSchemaName checks schema names are embedded in
// the function body safely, whatever characters they contain.
func TestNotifyFilterHostileSchemaName(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	schema := `we'ird$watch$\$$x`
	const name = "pdbcore_hostile_test"
	cleanup := func() {
		dropTriggers(pool, name)
		_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{Pool: pool, Schemas: []string{schema, "public"}, Channel: name, TriggerName: name}
	if err := w.installTrigger(ctx); err != nil {
		t.Fatal(err)
	}
	p := newNotifyProbe(t, pool, name)
	if !p.notifies("CREATE TABLE " + quoteIdent(schema) + ".t (id int)") {
		t.Fatal("DDL in the hostile-named watched schema did not notify")
	}
	if p.notifies("CREATE TEMP TABLE tt (id int)") {
		t.Fatal("temp table notified")
	}
}

func TestInstallTriggerUpgradesOldInstall(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const name = "pdbcore_upgrade_test"
	dropTriggers(pool, name)
	t.Cleanup(func() { dropTriggers(pool, name) })
	for _, s := range []string{
		`CREATE FUNCTION ` + name + `_notify() RETURNS event_trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_notify('` + name + `', tg_tag); END $$`,
		`CREATE EVENT TRIGGER ` + name + ` ON ddl_command_end EXECUTE FUNCTION ` + name + `_notify()`,
	} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	w := &Watcher{Pool: pool, Schemas: []string{"public"}, Channel: name, TriggerName: name}
	for range 2 {
		if err := w.installTrigger(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var src string
	if err := pool.QueryRow(ctx, "SELECT prosrc FROM pg_proc WHERE proname = $1", name+"_notify").Scan(&src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "pg_event_trigger_ddl_commands") {
		t.Fatalf("function body not replaced: %s", src)
	}
	rows, err := pool.Query(ctx, `SELECT evtname || ':' || evtevent FROM pg_event_trigger
		WHERE evtfoid = $1::regproc ORDER BY evtname`, name+"_notify")
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := name + ":ddl_command_end," + name + "_drop:sql_drop"
	if strings.Join(got, ",") != want {
		t.Fatalf("event triggers = %v, want %s", got, want)
	}
}

// logTap is a slog handler that forwards each message to a channel.
type logTap struct{ msgs chan string }

func (h logTap) Enabled(context.Context, slog.Level) bool { return true }
func (h logTap) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.msgs <- r.Message:
	default:
	}
	return nil
}
func (h logTap) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h logTap) WithGroup(string) slog.Handler      { return h }

func waitFor[T any](t *testing.T, ch <-chan T, what string, ok func(T) bool) T {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case v := <-ch:
			if ok(v) {
				return v
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestWatcherSkipsUnchangedCatalog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const schema, name = "watch_skip", "pdbcore_skip_test"
	cleanup := func() {
		dropTriggers(pool, name)
		_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, s := range []string{"CREATE SCHEMA " + schema, "CREATE TABLE " + schema + ".t (id int PRIMARY KEY)"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	boot, err := introspect.Introspect(ctx, pool, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := boot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	logs := make(chan string, 64)
	var mu sync.Mutex
	var changes []*introspect.Catalog
	changed := make(chan *introspect.Catalog, 8)
	w := &Watcher{
		Pool: pool, Schemas: []string{schema}, Channel: name, TriggerName: name,
		InitialHash: hash, Log: slog.New(logTap{logs}),
		OnChange: func(c *introspect.Catalog) {
			mu.Lock()
			changes = append(changes, c)
			mu.Unlock()
			changed <- c
		},
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		<-done
	})
	isUnchanged := func(m string) bool { return strings.HasPrefix(m, "watch: DDL left the catalog unchanged") }
	waitFor(t, logs, "the startup check", isUnchanged)

	if _, err := pool.Exec(ctx, "ALTER TABLE "+schema+".t ALTER COLUMN id SET STATISTICS 500"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, logs, "the hash check after SET STATISTICS", isUnchanged)
	mu.Lock()
	n := len(changes)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("OnChange called %d times for DDL that leaves the catalog unchanged", n)
	}

	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".probe (id int)"); err != nil {
		t.Fatal(err)
	}
	c := waitFor(t, changed, "OnChange after CREATE TABLE", func(*introspect.Catalog) bool { return true })
	if c.Table(schema, "probe") == nil {
		t.Fatal("catalog lacks the new table")
	}
	if _, err := pool.Exec(ctx, "DROP TABLE "+schema+".probe"); err != nil {
		t.Fatal(err)
	}
	c = waitFor(t, changed, "OnChange after DROP TABLE", func(*introspect.Catalog) bool { return true })
	if c.Table(schema, "probe") != nil {
		t.Fatal("catalog still has the dropped table")
	}
}

func TestRunDetectsChangeBeforeListening(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const schema, name = "watch_startup", "pdbcore_startup_test"
	cleanup := func() {
		dropTriggers(pool, name)
		_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	boot, err := introspect.Introspect(ctx, pool, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := boot.Hash()
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".late (id int)"); err != nil {
		t.Fatal(err)
	}
	changed := make(chan *introspect.Catalog, 1)
	w := &Watcher{
		Pool: pool, Schemas: []string{schema}, Channel: name, TriggerName: name, InitialHash: hash,
		Log:      slog.New(logTap{make(chan string)}),
		OnChange: func(c *introspect.Catalog) { changed <- c },
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	defer func() {
		stop()
		<-done
	}()
	c := waitFor(t, changed, "the startup OnChange", func(*introspect.Catalog) bool { return true })
	if c.Table(schema, "late") == nil {
		t.Fatal("startup catalog lacks the table created before Run")
	}
}

func TestPollComparesAgainstInitialHash(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const schema = "watch_poll"
	_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	boot, err := introspect.Introspect(ctx, pool, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := boot.Hash()
	changed := make(chan *introspect.Catalog, 4)
	w := &Watcher{
		Pool: pool, Schemas: []string{schema}, PollInterval: 50 * time.Millisecond,
		Log:      slog.New(logTap{make(chan string)}),
		OnChange: func(c *introspect.Catalog) { changed <- c },
	}
	w.lastHash = hash
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".early (id int)"); err != nil {
		t.Fatal(err)
	}
	pollCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.poll(pollCtx) }()
	c := waitFor(t, changed, "the first poll", func(*introspect.Catalog) bool { return true })
	if c.Table(schema, "early") == nil {
		t.Fatal("poll catalog lacks the table")
	}
	time.Sleep(200 * time.Millisecond)
	stop()
	<-done
	select {
	case <-changed:
		t.Fatal("OnChange called again with an unchanged catalog")
	default:
	}
}
