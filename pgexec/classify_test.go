package pgexec

import (
	"context"
	"errors"
	"strings"
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
		{"21000", UserInput}, {"22P02", UserInput}, {"22001", UserInput},
		{"23502", UserInput}, {"23514", UserInput},
		{"23505", Conflict}, {"23503", Conflict}, {"23001", Conflict}, {"23P01", Conflict},
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
	card := pgErr("21000", "ON CONFLICT DO UPDATE command cannot affect row a second time")

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
		{"cardinality prod passes through", card, Prod, card.Message},
		{"cardinality strict hidden", card, Strict, "invalid input value"},
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

func TestClassifyMessages(t *testing.T) {
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
		detail := Prod
		if tc.strict {
			detail = Strict
		}
		if got := Classify(pgErr(tc.code, tc.msg), detail).Message; got != tc.want {
			t.Errorf("Classify(%s, %v).Message = %q, want %q", tc.code, detail, got, tc.want)
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
	for in, want := range map[string]Detail{"dev": Dev, "prod": Prod, "strict": Strict} {
		got, err := ParseDetail(in)
		if err != nil || got != want {
			t.Errorf("ParseDetail(%q) = %v, %v, want %v", in, got, err, want)
		}
		if got.String() != in {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), in)
		}
	}
	for _, in := range []string{"", "x", "Dev"} {
		if _, err := ParseDetail(in); err == nil || !strings.Contains(err.Error(), "is not 'dev', 'prod' or 'strict'") {
			t.Errorf("ParseDetail(%q) error = %v", in, err)
		}
	}
}

func TestClassifyPostGISInputErrors(t *testing.T) {
	cases := []string{
		"ST_Intersects: Operation on mixed SRID geometries (Point, 4326) != (Point, 0)",
		"parse error - invalid geometry",
		"lwgeom_from_hexwkb: bad input",
	}
	for _, msg := range cases {
		e := pgErr("XX000", msg)
		if got := Classify(e, Prod); got.Class != UserInput || got.Message != "invalid geometry" {
			t.Errorf("Prod %q = %v %q", msg, got.Class, got.Message)
		}
		if got := Classify(e, Strict); got.Class != UserInput || got.Message != "invalid input value" {
			t.Errorf("Strict %q = %v %q", msg, got.Class, got.Message)
		}
		if got := Classify(e, Dev); got.Class != UserInput || got.Message != msg {
			t.Errorf("Dev %q = %v %q", msg, got.Class, got.Message)
		}
	}
	if got := Classify(pgErr("XX000", "something else"), Prod); got.Class != Internal || got.Message != "database error" {
		t.Errorf("unrelated XX000 = %v %q", got.Class, got.Message)
	}
	if got := Classify(pgErr("22023", "bad param"), Prod); got.Class != UserInput {
		t.Errorf("22023 = %v", got.Class)
	}
}

func TestClassifyRasterSRIDErrors(t *testing.T) {
	for _, code := range []string{"XX000", "P0001"} {
		e := pgErr(code, "Raster and geometry do not have the same SRID")
		if got := Classify(e, Prod); got.Class != UserInput || got.Message != e.Message {
			t.Errorf("%s prod = %v %q", code, got.Class, got.Message)
		}
		if got := Classify(e, Strict); got.Class != UserInput || got.Message != "invalid input value" {
			t.Errorf("%s strict = %v %q", code, got.Class, got.Message)
		}
	}
	if got := Classify(pgErr("P0001", "Raster and geometry do not have the same SRID!"), Prod); got.Class != Internal {
		t.Errorf("other raise = %v", got.Class)
	}
}

func TestClassifyContribInputErrors(t *testing.T) {
	user := []*pgconn.PgError{
		pgErr("42704", `text search configuration "klingon" does not exist`),
		pgErr("42601", `syntax error in tsquery: "a b"`),
		pgErr("42601", `no operand in tsquery: "a &"`),
		pgErr("42601", `syntax error in tsvector: "a:x"`),
		pgErr("42601", "ltree syntax error at character 3"),
		pgErr("42601", "lquery syntax error"),
		pgErr("42601", "lquery syntax error at character 3"),
		pgErr("42601", "operand syntax error"),
		pgErr("42601", "syntax error"),
	}
	for _, e := range user {
		if got := Classify(e, Prod); got.Class != UserInput || got.Message != e.Message {
			t.Errorf("Prod %s %q = %v %q", e.Code, e.Message, got.Class, got.Message)
		}
		if got := Classify(e, Strict); got.Class != UserInput || got.Message != "invalid input value" {
			t.Errorf("Strict %s %q = %v %q", e.Code, e.Message, got.Class, got.Message)
		}
	}
	statement := []*pgconn.PgError{
		pgErr("42601", `syntax error at or near "FROM"`),
		pgErr("42601", "syntax error at end of input"),
		pgErr("42601", "syntax error at end of jsonpath input"),
		pgErr("42704", `type "ltree" does not exist`),
	}
	for _, e := range statement {
		if got := Classify(e, Prod); got.Message != "invalid operation" {
			t.Errorf("%s %q = %v %q, want the generic 42* message", e.Code, e.Message, got.Class, got.Message)
		}
	}
}
