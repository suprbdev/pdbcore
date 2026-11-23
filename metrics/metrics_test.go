package metrics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/suprbdev/pdbcore/pgexec"
)

// scrape fetches h and returns every sample as series -> value, the series
// spelled as in the exposition (name{label="value",...}).
func scrape(t *testing.T, h http.Handler) map[string]float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("scrape content type %q", ct)
	}
	out := map[string]float64{}
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("bad sample %q: %v", line, err)
		}
		out[line[:i]] = v
	}
	return out
}

func mustNew(t *testing.T, o Options) *Metrics {
	t.Helper()
	if o.Namespace == "" {
		o.Namespace = "test"
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func want(t *testing.T, got map[string]float64, series string, v float64) {
	t.Helper()
	g, ok := got[series]
	if !ok {
		t.Errorf("series %s missing", series)
		return
	}
	if g != v {
		t.Errorf("%s = %v, want %v", series, g, v)
	}
}

func TestNewRequiresNamespace(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("want error without Namespace")
	}
}

func TestPrivateRegistries(t *testing.T) {
	a := mustNew(t, Options{Namespace: "pdbq"})
	b := mustNew(t, Options{Namespace: "pdbq"})
	a.SchemaRebuilt(nil)
	got := scrape(t, a.Handler())
	want(t, got, `pdbq_schema_rebuilds_total{result="success"}`, 1)
	want(t, got, `pdbq_schema_rebuilds_total{result="failure"}`, 0)
	if _, ok := got["go_goroutines"]; !ok {
		t.Error("private registry lacks the Go collector")
	}
	want(t, scrape(t, b.Handler()), `pdbq_schema_rebuilds_total{result="success"}`, 0)
}

func TestExternalRegisterer(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := mustNew(t, Options{Namespace: "pdbr", Registerer: reg, ConstLabels: prometheus.Labels{"instance_name": "a"}})
	if _, err := New(Options{Namespace: "pdbr", Registerer: reg, ConstLabels: prometheus.Labels{"instance_name": "a"}}); err == nil {
		t.Fatal("second Metrics with the same namespace and labels on one registry must fail")
	}
	other := mustNew(t, Options{Namespace: "pdbr", Registerer: reg, ConstLabels: prometheus.Labels{"instance_name": "b"}})
	m.ObserveCompile(time.Millisecond)
	other.ObserveCompile(time.Millisecond)
	other.ObserveCompile(time.Millisecond)
	got := scrape(t, m.Handler())
	want(t, got, `pdbr_compile_duration_seconds_count{instance_name="a"}`, 1)
	want(t, got, `pdbr_compile_duration_seconds_count{instance_name="b"}`, 2)
	if _, ok := got["go_goroutines"]; ok {
		t.Error("an external registry must not get the Go collector added")
	}
	if other.Registerer() == nil {
		t.Error("Registerer() = nil")
	}
}

type registererOnly struct{ prometheus.Registerer }

func TestCustomRegistererNeedsGatherer(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := New(Options{Namespace: "x", Registerer: registererOnly{reg}}); err == nil {
		t.Fatal("want error for a Registerer that is not a Gatherer")
	}
	m := mustNew(t, Options{Namespace: "x", Registerer: registererOnly{reg}, Gatherer: reg})
	m.SchemaRebuilt(errors.New("boom"))
	want(t, scrape(t, m.Handler()), `x_schema_rebuilds_total{result="failure"}`, 1)
}

func TestNilMetricsIsNoOp(t *testing.T) {
	var m *Metrics
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if got := m.Middleware(h, nil); fmt.Sprintf("%p", got) != fmt.Sprintf("%p", h) {
		t.Error("Middleware on nil Metrics must return next")
	}
	if m.DBObserver() != nil {
		t.Error("DBObserver on nil Metrics must be a nil interface")
	}
	if m.Registerer() != nil {
		t.Error("Registerer on nil Metrics must be nil")
	}
	if err := m.RegisterPool(nil); err != nil {
		t.Error(err)
	}
	m.SchemaRebuilt(nil)
	m.ObserveCompile(time.Second)
	c, err := m.NewCounter("x_total", "x", "a")
	if err != nil || c != nil {
		t.Fatalf("NewCounter = %v, %v", c, err)
	}
	c.Inc("a")
	c.Add(2, "a")
	c.Init("a")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("nil Handler status %d", rec.Code)
	}
}

func TestMiddleware(t *testing.T) {
	m := mustNew(t, Options{})
	var inFlight float64
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		inFlight = scrape(t, m.Handler())["test_http_requests_in_flight"]
		SetRoute(r.Context(), "graphql")
		w.WriteHeader(http.StatusBadRequest)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/empty", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/early", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	classify := func(r *http.Request) string {
		if r.URL.Path == "/ok" {
			return "health"
		}
		return ""
	}
	srv := httptest.NewServer(m.Middleware(mux, classify))
	defer srv.Close()
	do := func(method, path string) {
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	do(http.MethodPost, "/graphql")
	do(http.MethodGet, "/ok")
	do(http.MethodGet, "/ok")
	do("BREW", "/empty")
	do(http.MethodGet, "/early")
	do(http.MethodGet, "/missing")

	got := scrape(t, m.Handler())
	if inFlight != 1 {
		t.Errorf("in flight during a request = %v, want 1", inFlight)
	}
	want(t, got, "test_http_requests_in_flight", 0)
	want(t, got, `test_http_requests_total{method="POST",route="graphql",status_class="4xx"}`, 1)
	want(t, got, `test_http_requests_total{method="GET",route="health",status_class="2xx"}`, 2)
	want(t, got, `test_http_requests_total{method="other",route="other",status_class="2xx"}`, 1)
	want(t, got, `test_http_requests_total{method="GET",route="other",status_class="5xx"}`, 1)
	want(t, got, `test_http_requests_total{method="GET",route="other",status_class="4xx"}`, 1)
	want(t, got, `test_http_request_duration_seconds_count{method="GET",route="health",status_class="2xx"}`, 2)
}

func TestStatusWriterKeepsOptionalInterfaces(t *testing.T) {
	m := mustNew(t, Options{})
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through ResponseController: %v", err)
		}
		if f, ok := w.(http.Flusher); !ok {
			t.Error("wrapped writer is not an http.Flusher")
		} else {
			f.Flush()
		}
	}), func(*http.Request) string { return "stream" })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !rec.Flushed {
		t.Fatal("flush did not reach the recorder")
	}
	want(t, scrape(t, m.Handler()), `test_http_requests_total{method="GET",route="stream",status_class="2xx"}`, 1)
}

func TestSetRouteOutsideMiddleware(t *testing.T) {
	SetRoute(context.Background(), "x")
}

func TestStatusClass(t *testing.T) {
	for code, w := range map[int]string{100: "1xx", 200: "2xx", 204: "2xx", 304: "3xx", 404: "4xx", 503: "5xx", 99: "other", 600: "other"} {
		if got := statusClass(code); got != w {
			t.Errorf("statusClass(%d) = %q, want %q", code, got, w)
		}
	}
}

func TestDBObserver(t *testing.T) {
	m := mustNew(t, Options{})
	o := m.DBObserver()
	ctx := context.Background()
	o.ObserveQuery(ctx, pgexec.QueryEvent{Op: pgexec.OpQueryTx, Duration: 2 * time.Millisecond})
	o.ObserveQuery(ctx, pgexec.QueryEvent{Op: pgexec.OpQueryTx, Duration: time.Millisecond,
		Err: &pgconn.PgError{Code: "40001"}})
	o.ObserveQuery(ctx, pgexec.QueryEvent{Op: pgexec.OpQueryTx, Attempt: 1, Duration: time.Millisecond,
		Err: fmt.Errorf("set identity: %w", &pgconn.PgError{Code: "42501"})})
	pgexec.Observe(ctx, o, "query", time.Now(), context.DeadlineExceeded)
	pgexec.Observe(ctx, o, "query", time.Now(), fmt.Errorf("scan: %w", pgx.ErrNoRows))
	o.ObserveQuery(ctx, pgexec.QueryEvent{})

	got := scrape(t, m.Handler())
	want(t, got, `test_db_duration_seconds_count{op="query_tx"}`, 3)
	want(t, got, `test_db_duration_seconds_count{op="query"}`, 2)
	if _, ok := got[`test_db_errors_total{op="query",sqlstate_class="other"}`]; ok {
		t.Error("pgx.ErrNoRows counted as an error")
	}
	want(t, got, `test_db_duration_seconds_count{op="unknown"}`, 1)
	want(t, got, `test_db_errors_total{op="query_tx",sqlstate_class="40"}`, 1)
	want(t, got, `test_db_errors_total{op="query_tx",sqlstate_class="42"}`, 1)
	want(t, got, `test_db_errors_total{op="query",sqlstate_class="timeout"}`, 1)
	want(t, got, `test_db_retries_total{op="query_tx"}`, 1)
}

func TestSQLStateClass(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{&pgconn.PgError{Code: "23505"}, "23"},
		{fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "57014"}), "57"},
		{&pgconn.PgError{Code: "x"}, "other"},
		{context.Canceled, "canceled"},
		{fmt.Errorf("wrap: %w", context.DeadlineExceeded), "timeout"},
		{&pgconn.ConnectError{}, "other"},
		{errors.New("boom"), "other"},
	}
	for _, tc := range cases {
		if got := SQLStateClass(tc.err); got != tc.want {
			t.Errorf("SQLStateClass(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestCounter(t *testing.T) {
	m := mustNew(t, Options{})
	c, err := m.NewCounter("document_cache_requests_total", "Document cache lookups.", "result")
	if err != nil {
		t.Fatal(err)
	}
	c.Init("miss")
	c.Inc("hit")
	c.Add(2, "hit")
	plain, err := m.NewCounter("cost_limit_rejections_total", "Rejected requests.")
	if err != nil {
		t.Fatal(err)
	}
	want(t, scrape(t, m.Handler()), `test_cost_limit_rejections_total`, 0)
	plain.Inc()
	if _, err := m.NewCounter("cost_limit_rejections_total", "again"); err == nil {
		t.Fatal("duplicate counter must fail")
	}
	got := scrape(t, m.Handler())
	want(t, got, `test_document_cache_requests_total{result="hit"}`, 3)
	want(t, got, `test_document_cache_requests_total{result="miss"}`, 0)
	want(t, got, `test_cost_limit_rejections_total`, 1)
}

func TestSchemaRebuiltAndCompile(t *testing.T) {
	m := mustNew(t, Options{DurationBuckets: []float64{0.001, 1}})
	want(t, scrape(t, m.Handler()), "test_schema_last_rebuild_timestamp_seconds", 0)
	before := float64(time.Now().Unix())
	m.SchemaRebuilt(nil)
	m.SchemaRebuilt(errors.New("bad catalog"))
	m.ObserveCompile(500 * time.Microsecond)
	m.ObserveCompile(2 * time.Second)
	got := scrape(t, m.Handler())
	if ts := got["test_schema_last_rebuild_timestamp_seconds"]; ts < before {
		t.Errorf("last rebuild timestamp %v before %v", ts, before)
	}
	want(t, got, `test_schema_rebuilds_total{result="success"}`, 1)
	want(t, got, `test_schema_rebuilds_total{result="failure"}`, 1)
	want(t, got, `test_compile_duration_seconds_bucket{le="0.001"}`, 1)
	want(t, got, `test_compile_duration_seconds_bucket{le="1"}`, 1)
	want(t, got, `test_compile_duration_seconds_bucket{le="+Inf"}`, 2)
}

func TestRegisterPoolUnconnected(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 7
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m := mustNew(t, Options{})
	if err := m.RegisterPool((*pgxpool.Pool)(nil)); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterPool(pool); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterPool(pool); err == nil {
		t.Fatal("second pool must fail")
	}
	got := scrape(t, m.Handler())
	want(t, got, "test_db_pool_max_conns", 7)
	want(t, got, "test_db_pool_total_conns", 0)
	want(t, got, "test_db_pool_acquires_total", 0)
	for _, s := range []string{
		"test_db_pool_acquired_conns", "test_db_pool_idle_conns", "test_db_pool_constructing_conns",
		"test_db_pool_canceled_acquires_total", "test_db_pool_empty_acquires_total",
		"test_db_pool_acquire_duration_seconds_total", "test_db_pool_empty_acquire_wait_seconds_total",
		"test_db_pool_new_conns_total", "test_db_pool_max_lifetime_destroys_total", "test_db_pool_max_idle_destroys_total",
	} {
		if _, ok := got[s]; !ok {
			t.Errorf("series %s missing", s)
		}
	}
}

func TestSharedRegistryPerContract(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	public := mustNew(t, Options{Namespace: "pdbq", Registerer: reg, ConstLabels: prometheus.Labels{"contract": "public"}})
	partner := mustNew(t, Options{Namespace: "pdbq", Registerer: reg, ConstLabels: prometheus.Labels{"contract": "partner"}})
	cfg, err := pgxpool.ParseConfig("postgres://u@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := RegisterPoolOn(reg, "pdbq", pool); err != nil {
		t.Fatal(err)
	}
	if err := RegisterPoolOn(reg, "pdbq", (*pgxpool.Pool)(nil)); err != nil {
		t.Fatal(err)
	}
	public.SchemaRebuilt(nil)
	partner.SchemaRebuilt(errors.New("boom"))
	got := scrape(t, partner.Handler())
	want(t, got, `pdbq_schema_rebuilds_total{contract="public",result="success"}`, 1)
	want(t, got, `pdbq_schema_rebuilds_total{contract="partner",result="failure"}`, 1)
	want(t, got, "pdbq_db_pool_max_conns", 3)
	if _, ok := got["go_goroutines"]; !ok {
		t.Error("NewRegistry lacks the Go collector")
	}
}
