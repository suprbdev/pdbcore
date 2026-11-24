package pgexec

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeTx records the statements executed inside one attempt. Only the
// methods RunTx touches are implemented; the embedded interface panics on
// anything else, which is the point.
type fakeTx struct {
	pgx.Tx
	stmts     []string
	committed bool
	rolled    bool
	execErr   func(sql string) error
	commitErr error
}

func (f *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	rendered := sql
	if len(args) > 0 {
		parts := make([]string, len(args))
		for i, a := range args {
			switch v := a.(type) {
			case string:
				parts[i] = v
			case []string:
				parts[i] = "{" + strings.Join(v, ",") + "}"
			}
		}
		rendered += " [" + strings.Join(parts, ", ") + "]"
	}
	f.stmts = append(f.stmts, rendered)
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr(sql)
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeTx) Commit(context.Context) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	f.committed = true
	return nil
}

func (f *fakeTx) Rollback(context.Context) error {
	if !f.committed {
		f.rolled = true
	}
	return nil
}

type fakeDB struct {
	txs      []*fakeTx
	beginErr error
	isoSeen  []pgx.TxIsoLevel
	newTx    func() *fakeTx
}

func (d *fakeDB) BeginTx(_ context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	d.isoSeen = append(d.isoSeen, o.IsoLevel)
	tx := &fakeTx{}
	if d.newTx != nil {
		tx = d.newTx()
	}
	d.txs = append(d.txs, tx)
	return tx, nil
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func serFail() error {
	return &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
}

func TestRunTxAppliesIdentityThenCommits(t *testing.T) {
	db := &fakeDB{}
	id := Identity{
		Role:         "app_user",
		ClaimsPrefix: "jwt.claims",
		Claims:       map[string]any{"user_id": "7", "roles": []string{"a"}, "bad-key": "x"},
	}
	calls := 0
	err := RunTx(context.Background(), db, TxOptions{Isolation: pgx.Serializable, RLS: true}, id, func(q Querier) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(db.txs) != 1 || db.isoSeen[0] != pgx.Serializable {
		t.Fatalf("calls=%d txs=%d iso=%v", calls, len(db.txs), db.isoSeen)
	}
	tx := db.txs[0]
	if !tx.committed || tx.rolled {
		t.Fatalf("committed=%v rolled=%v", tx.committed, tx.rolled)
	}
	if tx.stmts[0] != `SET LOCAL ROLE "app_user"` {
		t.Fatalf("first stmt = %q", tx.stmts[0])
	}
	want := claimsSQL + ` [{jwt.claims.roles,jwt.claims.user_id}, {["a"],7}]`
	if len(tx.stmts) != 2 || tx.stmts[1] != want {
		t.Fatalf("stmts = %q, want role then one claims statement %q", tx.stmts, want)
	}
}

func TestApplyIdentityNoClaimsNoStatement(t *testing.T) {
	tx := &fakeTx{}
	if err := ApplyIdentity(context.Background(), tx, Identity{ClaimsPrefix: "jwt.claims", Claims: map[string]any{"bad-key": "x"}}); err != nil {
		t.Fatal(err)
	}
	if len(tx.stmts) != 0 {
		t.Fatalf("stmts = %q, want none", tx.stmts)
	}
}

func TestRunTxCustomPrefixAndNoRLS(t *testing.T) {
	db := &fakeDB{}
	id := Identity{Role: "r", Claims: map[string]any{"k": "v"}, ClaimsPrefix: "pdbr.claims"}
	if err := RunTx(context.Background(), db, TxOptions{RLS: true}, id, func(Querier) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := db.txs[0].stmts[1]; got != claimsSQL+` [{pdbr.claims.k}, {v}]` {
		t.Fatalf("stmt = %q", got)
	}
	db = &fakeDB{}
	if err := RunTx(context.Background(), db, TxOptions{RLS: false}, id, func(Querier) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(db.txs[0].stmts) != 0 {
		t.Fatalf("RLS off must issue no identity statements, got %v", db.txs[0].stmts)
	}
}

func TestRunTxInvalidRole(t *testing.T) {
	db := &fakeDB{}
	err := RunTx(context.Background(), db, TxOptions{RLS: true}, Identity{Role: "app;user"}, func(Querier) error {
		t.Fatal("fn must not run")
		return nil
	})
	if err == nil || err.Error() != `invalid role "app;user"` {
		t.Fatalf("err = %v", err)
	}
	if !db.txs[0].rolled {
		t.Fatal("expected rollback")
	}
}

func TestRunTxRejectsRoleNoneAndReservedPrefix(t *testing.T) {
	for _, id := range []Identity{
		{Role: "none"},
		{Role: "pg_read_all_data"},
		{Role: "app_user", Claims: map[string]any{"variable_conflict": "use_column"}, ClaimsPrefix: "plpgsql"},
	} {
		db := &fakeDB{}
		err := RunTx(context.Background(), db, TxOptions{RLS: true}, id, func(Querier) error {
			t.Fatalf("fn must not run for %+v", id)
			return nil
		})
		if err == nil || !strings.HasPrefix(err.Error(), "invalid ") {
			t.Fatalf("%+v: err = %v", id, err)
		}
		if len(db.txs[0].stmts) != 0 || !db.txs[0].rolled {
			t.Fatalf("%+v: stmts=%q rolled=%v, want no statement and a rollback", id, db.txs[0].stmts, db.txs[0].rolled)
		}
	}
}

func TestRunTxRollsBackOnFnErrorUnwrapped(t *testing.T) {
	db := &fakeDB{}
	sentinel := errors.New("mine")
	err := RunTx(context.Background(), db, TxOptions{}, Identity{}, func(Querier) error { return sentinel })
	if err != sentinel { //nolint:errorlint // identity is the contract: callers get their error back as-is
		t.Fatalf("err = %v, want the sentinel unwrapped", err)
	}
	if db.txs[0].committed || !db.txs[0].rolled {
		t.Fatal("expected rollback, no commit")
	}
}

func TestRunTxRetriesSerializationFailure(t *testing.T) {
	db := &fakeDB{}
	attempts := 0
	err := RunTx(context.Background(), db, TxOptions{MaxRetries: 2, Log: quiet}, Identity{}, func(Querier) error {
		attempts++
		if attempts < 3 {
			return serFail()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || len(db.txs) != 3 {
		t.Fatalf("attempts=%d txs=%d", attempts, len(db.txs))
	}
	if !db.txs[0].rolled || !db.txs[1].rolled || !db.txs[2].committed {
		t.Fatal("first two attempts must roll back, third commit")
	}
}

func TestRunTxRetryExhaustedReturnsLastError(t *testing.T) {
	db := &fakeDB{}
	attempts := 0
	err := RunTx(context.Background(), db, TxOptions{MaxRetries: 1, Log: quiet}, Identity{}, func(Querier) error {
		attempts++
		return serFail()
	})
	if !IsSerializationFailure(err) || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRunTxNoRetryWithoutBudgetOrOnOtherErrors(t *testing.T) {
	db := &fakeDB{}
	attempts := 0
	_ = RunTx(context.Background(), db, TxOptions{}, Identity{}, func(Querier) error {
		attempts++
		return serFail()
	})
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 without retries", attempts)
	}
	db = &fakeDB{}
	attempts = 0
	_ = RunTx(context.Background(), db, TxOptions{MaxRetries: 3, Log: quiet}, Identity{}, func(Querier) error {
		attempts++
		return &pgconn.PgError{Code: "23505"}
	})
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 for non-serialization errors", attempts)
	}
}

func TestRunTxRetriesWrappedSerializationFailure(t *testing.T) {
	db := &fakeDB{}
	attempts := 0
	err := RunTx(context.Background(), db, TxOptions{MaxRetries: 1, Log: quiet}, Identity{}, func(Querier) error {
		attempts++
		if attempts == 1 {
			return errors.Join(errors.New("field x"), serFail())
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRunTxCommitErrorSurfaces(t *testing.T) {
	commitErr := errors.New("commit failed")
	db := &fakeDB{newTx: func() *fakeTx { return &fakeTx{commitErr: commitErr} }}
	err := RunTx(context.Background(), db, TxOptions{}, Identity{}, func(Querier) error { return nil })
	if !errors.Is(err, commitErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTxBeginError(t *testing.T) {
	beginErr := errors.New("no conn")
	db := &fakeDB{beginErr: beginErr}
	err := RunTx(context.Background(), db, TxOptions{}, Identity{}, func(Querier) error {
		t.Fatal("fn must not run")
		return nil
	})
	if !errors.Is(err, beginErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTxStopsRetryingOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := &fakeDB{}
	attempts := 0
	_ = RunTx(ctx, db, TxOptions{MaxRetries: 5, Log: quiet}, Identity{}, func(Querier) error {
		attempts++
		cancel()
		return serFail()
	})
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 after cancellation", attempts)
	}
}

func TestIsSerializationFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"40001", &pgconn.PgError{Code: "40001"}, true},
		{"40P01", &pgconn.PgError{Code: "40P01"}, true},
		{"23505", &pgconn.PgError{Code: "23505"}, false},
		{"plain", errors.New("x"), false},
	}
	for _, tc := range cases {
		if got := IsSerializationFailure(tc.err); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseIsolation(t *testing.T) {
	cases := map[string]pgx.TxIsoLevel{
		"read_committed": pgx.ReadCommitted, "repeatable_read": pgx.RepeatableRead,
		"serializable": pgx.Serializable,
	}
	for in, want := range cases {
		if got, err := ParseIsolation(in); err != nil || got != want {
			t.Errorf("ParseIsolation(%q) = %v, %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "chaos", "SERIALIZABLE"} {
		if _, err := ParseIsolation(in); err == nil {
			t.Errorf("ParseIsolation(%q) accepted", in)
		}
	}
}

func TestApplyIdentityClaimsJSONSetting(t *testing.T) {
	cases := []struct {
		name string
		id   Identity
		want string
	}{
		{"json only", Identity{ClaimsJSONSetting: "request.jwt.claims", Claims: map[string]any{"sub": "u1", "n": 2}},
			claimsSQL + ` [{request.jwt.claims}, {{"n":2,"sub":"u1"}}]`},
		{"prefix and json", Identity{ClaimsPrefix: "jwt.claims", ClaimsJSONSetting: "request.jwt.claims", Claims: map[string]any{"bad-key": "x", "sub": "u1"}},
			claimsSQL + ` [{jwt.claims.sub,request.jwt.claims}, {u1,{"bad-key":"x","sub":"u1"}}]`},
		{"anonymous gets an empty object", Identity{ClaimsPrefix: "jwt.claims", ClaimsJSONSetting: "request.jwt.claims"},
			claimsSQL + ` [{request.jwt.claims}, {{}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{}
			if err := ApplyIdentity(context.Background(), tx, tc.id); err != nil {
				t.Fatal(err)
			}
			if len(tx.stmts) != 1 || tx.stmts[0] != tc.want {
				t.Fatalf("stmts = %q, want [%q]", tx.stmts, tc.want)
			}
		})
	}
}

func TestApplyIdentityNoPrefixSetsNoClaims(t *testing.T) {
	tx := &fakeTx{}
	if err := ApplyIdentity(context.Background(), tx, Identity{Claims: map[string]any{"sub": "u1"}}); err != nil {
		t.Fatal(err)
	}
	if len(tx.stmts) != 0 {
		t.Fatalf("stmts = %q, want none", tx.stmts)
	}
}

func TestApplyIdentityRejectsBadClaimsSetting(t *testing.T) {
	for _, name := range []string{"claims", "plpgsql.claims", "pg_x.y", "a..b"} {
		err := ApplyIdentity(context.Background(), &fakeTx{}, Identity{ClaimsJSONSetting: name})
		if err == nil || !strings.HasPrefix(err.Error(), "invalid claims setting") {
			t.Errorf("%q: err = %v", name, err)
		}
	}
}
