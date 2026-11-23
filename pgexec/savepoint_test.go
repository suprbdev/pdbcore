package pgexec

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type spQuerier struct {
	Querier
	stmts []string
	fail  map[string]error
}

func (s *spQuerier) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.stmts = append(s.stmts, sql)
	return pgconn.CommandTag{}, s.fail[sql]
}

func TestSavepoint(t *testing.T) {
	boom := errors.New("boom")
	serial := &pgconn.PgError{Code: "40001"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	rbFail := errors.New("rollback failed")
	tests := []struct {
		name      string
		ctx       context.Context
		fn        error
		fail      map[string]error
		wantStmts []string
		wantErr   error
		wantRB    bool
	}{
		{name: "success releases", fn: nil,
			wantStmts: []string{"SAVEPOINT pdb_savepoint", "RELEASE SAVEPOINT pdb_savepoint"}},
		{name: "failure rolls back and releases", fn: boom, wantRB: true, wantErr: boom,
			wantStmts: []string{"SAVEPOINT pdb_savepoint", "ROLLBACK TO SAVEPOINT pdb_savepoint", "RELEASE SAVEPOINT pdb_savepoint"}},
		{name: "serialization failure is fatal", fn: serial, wantErr: serial,
			wantStmts: []string{"SAVEPOINT pdb_savepoint"}},
		{name: "requested rollback is fatal", fn: ErrRollback, wantErr: ErrRollback,
			wantStmts: []string{"SAVEPOINT pdb_savepoint"}},
		{name: "done context is fatal", ctx: cancelled, fn: boom, wantErr: boom,
			wantStmts: []string{"SAVEPOINT pdb_savepoint"}},
		{name: "rollback failure is fatal", fn: boom,
			fail:      map[string]error{"ROLLBACK TO SAVEPOINT pdb_savepoint": rbFail},
			wantErr:   rbFail,
			wantStmts: []string{"SAVEPOINT pdb_savepoint", "ROLLBACK TO SAVEPOINT pdb_savepoint"}},
		{name: "savepoint failure is fatal and skips fn", fn: nil,
			fail:      map[string]error{"SAVEPOINT pdb_savepoint": rbFail},
			wantErr:   rbFail,
			wantStmts: []string{"SAVEPOINT pdb_savepoint"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			q := &spQuerier{fail: tc.fail}
			called := false
			err := Savepoint(ctx, q, func() error { called = true; return tc.fn })
			if tc.name == "savepoint failure is fatal and skips fn" && called {
				t.Fatal("fn ran after SAVEPOINT failed")
			}
			if !errors.Is(err, tc.wantErr) && tc.wantErr != nil {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("err = %v", err)
			}
			var rb *RolledBackError
			if got := errors.As(err, &rb); got != tc.wantRB {
				t.Fatalf("RolledBackError = %v, want %v (err %v)", got, tc.wantRB, err)
			}
			if len(q.stmts) != len(tc.wantStmts) {
				t.Fatalf("stmts = %v, want %v", q.stmts, tc.wantStmts)
			}
			for i := range q.stmts {
				if q.stmts[i] != tc.wantStmts[i] {
					t.Fatalf("stmts = %v, want %v", q.stmts, tc.wantStmts)
				}
			}
		})
	}
}
