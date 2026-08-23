package pgexec

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func pgErr(code, msg string) *pgconn.PgError {
	return &pgconn.PgError{Code: code, Message: msg}
}

func TestClassifyClasses(t *testing.T) {
	cases := []struct {
		code string
		want Class
	}{
		{"22P02", UserInput}, {"22001", UserInput},
		{"23502", UserInput}, {"23514", UserInput},
		{"23505", Conflict}, {"23503", Conflict}, {"23P01", Conflict},
		{"23000", Internal},
		{"40001", Conflict}, {"40P01", Conflict},
		{"42501", Permission}, {"42703", UserInput}, {"42601", UserInput},
		{"53300", Resource}, {"08006", Resource}, {"57P01", Resource},
		{"57014", Timeout},
		{"P0001", Internal}, {"XX000", Internal}, {"", Internal},
	}
	for _, tc := range cases {
		if got := Classify(pgErr(tc.code, "m"), Prod).Class; got != tc.want {
			t.Errorf("Classify(%q).Class = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestClassifyMessagePolicy(t *testing.T) {
	uniq := pgErr("23505", `duplicate key value violates unique constraint "users_email_key"`)
	uniq.ConstraintName = "users_email_key"
	bad := pgErr("22P02", `invalid input syntax for type integer: "abc" in column "salary"`)
	perm := pgErr("42501", `permission denied for table users`)
	undef := pgErr("42703", `column "secret" does not exist`)
	raise := pgErr("P0001", "nope")

	cases := []struct {
		name   string
		err    *pgconn.PgError
		detail Detail
		want   string
	}{
		{"unique prod passes through", uniq, Prod, uniq.Message},
		{"unique strict hidden", uniq, Strict, "constraint violation"},
		{"unique dev full", uniq, Dev, uniq.Message},
		{"data exception prod passes through", bad, Prod, bad.Message},
		{"data exception strict hidden", bad, Strict, "invalid input value"},
		{"permission prod", perm, Prod, "permission denied"},
		{"permission strict", perm, Strict, "permission denied"},
		{"permission dev", perm, Dev, perm.Message},
		{"undefined column prod", undef, Prod, "invalid operation"},
		{"raise prod", raise, Prod, "database error"},
		{"raise strict", raise, Strict, "database error"},
		{"raise dev", raise, Dev, "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Classify(tc.err, tc.detail)
			if c.Message != tc.want {
				t.Fatalf("Message = %q, want %q", c.Message, tc.want)
			}
			if c.SQLState != tc.err.Code || c.PG != tc.err {
				t.Fatalf("SQLState/PG not carried: %+v", c)
			}
		})
	}
}

// SanitizeMessage must stay byte-identical to pdbq's sanitizePGMessage.
func TestSanitizeMessageMatchesPdbq(t *testing.T) {
	cases := []struct {
		code, msg string
		strict    bool
		want      string
	}{
		{"23505", "dup", false, "dup"},
		{"23505", "dup", true, "constraint violation"},
		{"22003", "range", false, "range"},
		{"22003", "range", true, "invalid input value"},
		{"42501", "denied", false, "permission denied"},
		{"42P01", "no table", false, "invalid operation"},
		{"40001", "ser", false, "database error"},
		{"P0001", "raise", true, "database error"},
	}
	for _, tc := range cases {
		if got := SanitizeMessage(pgErr(tc.code, tc.msg), tc.strict); got != tc.want {
			t.Errorf("SanitizeMessage(%s, strict=%v) = %q, want %q", tc.code, tc.strict, got, tc.want)
		}
	}
}

func TestClassifyNonPG(t *testing.T) {
	if c := Classify(nil, Prod); c.Class != OK || c.Message != "" {
		t.Fatalf("nil = %+v", c)
	}
	boom := errors.New("pool: connection refused to host db.internal")
	if c := Classify(boom, Prod); c.Class != Internal || c.Message != "internal error" || c.PG != nil {
		t.Fatalf("prod non-pg = %+v", c)
	}
	if c := Classify(boom, Strict); c.Message != "internal error" {
		t.Fatalf("strict non-pg = %+v", c)
	}
	if c := Classify(boom, Dev); c.Message != boom.Error() {
		t.Fatalf("dev non-pg = %+v", c)
	}
	if c := Classify(context.DeadlineExceeded, Prod); c.Class != Timeout {
		t.Fatalf("deadline = %+v", c)
	}
	wrapped := errors.Join(errors.New("outer"), pgErr("23505", "dup"))
	if c := Classify(wrapped, Prod); c.Class != Conflict || c.SQLState != "23505" {
		t.Fatalf("wrapped pg error not found: %+v", c)
	}
}

func TestParseDetail(t *testing.T) {
	for in, want := range map[string]Detail{"dev": Dev, "prod": Prod, "strict": Strict, "": Prod, "x": Prod} {
		if got := ParseDetail(in); got != want {
			t.Errorf("ParseDetail(%q) = %v, want %v", in, got, want)
		}
	}
}
