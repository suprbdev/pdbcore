package pgexec

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func queued(b *pgx.Batch) []string {
	out := make([]string, len(b.QueuedQueries))
	for i, q := range b.QueuedQueries {
		out[i] = q.SQL
	}
	return out
}

func TestBatchPlanShapes(t *testing.T) {
	stmt := []Stmt{{SQL: "SELECT 1"}, {SQL: "SELECT 2"}}
	anon := Identity{Role: "anonymous"}
	cases := []struct {
		name string
		opts TxOptions
		id   Identity
		want []string
	}{
		{"implicit, no rls", TxOptions{}, anon, []string{"SELECT 1", "SELECT 2"}},
		{"implicit, role", TxOptions{RLS: true}, anon, []string{identitySQL, "SELECT 1", "SELECT 2"}},
		{"implicit, claims only", TxOptions{RLS: true}, Identity{Claims: map[string]any{"sub": "1"}}, []string{claimsSQL, "SELECT 1", "SELECT 2"}},
		{"implicit, empty identity", TxOptions{RLS: true}, Identity{}, []string{"SELECT 1", "SELECT 2"}},
		{"explicit", TxOptions{RLS: true, Isolation: pgx.Serializable}, anon,
			[]string{"BEGIN ISOLATION LEVEL SERIALIZABLE", identitySQL, "SELECT 1", "SELECT 2", "COMMIT"}},
		{"explicit read committed", TxOptions{Isolation: pgx.ReadCommitted}, anon,
			[]string{"BEGIN ISOLATION LEVEL READ COMMITTED", "SELECT 1", "SELECT 2", "COMMIT"}},
		{"scan before commit, default isolation", TxOptions{RLS: true, ScanBeforeCommit: true}, anon,
			[]string{"BEGIN", identitySQL, "SELECT 1", "SELECT 2"}},
		{"scan before commit, repeatable read", TxOptions{ScanBeforeCommit: true, Isolation: pgx.RepeatableRead}, anon,
			[]string{"BEGIN ISOLATION LEVEL REPEATABLE READ", "SELECT 1", "SELECT 2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := newBatchPlan(tc.opts, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if got := queued(p.batch(stmt)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("batch:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			if p.commitAfter != tc.opts.ScanBeforeCommit {
				t.Fatalf("commitAfter = %v", p.commitAfter)
			}
		})
	}
}

func TestBatchPlanIdentityArgs(t *testing.T) {
	p, err := newBatchPlan(TxOptions{RLS: true}, Identity{
		Role:         "app_user",
		Claims:       map[string]any{"user_id": "7", "roles": []string{"a"}, "exp": 12, "bad-key": "x", "fn": func() {}},
		ClaimsPrefix: "pdbr.claims",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"app_user",
		[]string{"pdbr.claims.exp", "pdbr.claims.roles", "pdbr.claims.user_id"},
		[]string{"12", `["a"]`, "7"}}
	if p.identity != identitySQL || !reflect.DeepEqual(p.identityArgs, want) {
		t.Fatalf("identity %q args %#v", p.identity, p.identityArgs)
	}
	p, err = newBatchPlan(TxOptions{RLS: true}, Identity{Role: "anonymous"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.identityArgs, []any{"anonymous", []string{}, []string{}}) {
		t.Fatalf("role-only args %#v", p.identityArgs)
	}
}

func TestBatchPlanRejects(t *testing.T) {
	if _, err := newBatchPlan(TxOptions{RLS: true}, Identity{Role: `x"; RESET ROLE; --`}); err == nil || !strings.HasPrefix(err.Error(), "invalid role") {
		t.Fatalf("invalid role err = %v", err)
	}
	if _, err := newBatchPlan(TxOptions{RLS: false}, Identity{Role: `x"`}); err != nil {
		t.Fatalf("role is ignored without RLS, got %v", err)
	}
	if _, err := newBatchPlan(TxOptions{Isolation: "chaos; DROP TABLE x"}, Identity{}); err == nil || !strings.HasPrefix(err.Error(), "invalid isolation level") {
		t.Fatalf("invalid isolation err = %v", err)
	}
}

type noAcquire struct{ t *testing.T }

func (n noAcquire) Acquire(context.Context) (*pgxpool.Conn, error) {
	n.t.Fatal("QueryTx must not acquire a connection for an invalid request")
	return nil, nil
}

func TestQueryTxValidatesBeforeAcquiring(t *testing.T) {
	err := QueryTx(context.Background(), noAcquire{t}, TxOptions{RLS: true}, Identity{Role: "app;user"}, Stmt{SQL: "SELECT 1"})
	if err == nil || err.Error() != `invalid role "app;user"` {
		t.Fatalf("err = %v", err)
	}
}
