package pgexec

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Class is the transport-neutral error category. Products map it to an HTTP
// status (pdbr) or a GraphQL error shape (pdbq); the mapping table itself is
// product code.
type Class int

const (
	// OK: err was nil.
	OK Class = iota
	// UserInput: 22* data exceptions, 23502/23514, and 42* other than 42501.
	UserInput
	// Permission: 42501 insufficient privilege.
	Permission
	// Conflict: 23505/23503/23P01 and 40001/40P01 (retries exhausted).
	Conflict
	// Resource: 53* insufficient resources, 08* connection exceptions,
	// 57P01-57P03 operator intervention, connect failures.
	Resource
	// Timeout: 57014 statement timeout or a context deadline.
	Timeout
	// Internal: everything else.
	Internal
)

func (c Class) String() string {
	switch c {
	case OK:
		return "ok"
	case UserInput:
		return "user_input"
	case Permission:
		return "permission"
	case Conflict:
		return "conflict"
	case Resource:
		return "resource"
	case Timeout:
		return "timeout"
	}
	return "internal"
}

// Detail is the errors.detail policy.
type Detail int

const (
	// Prod passes 22*/23* PG messages through and hides the rest.
	Prod Detail = iota
	// Dev exposes full PG messages (and non-PG error text).
	Dev
	// Strict hides every database message behind a generic one.
	Strict
)

// ParseDetail maps the config strings "dev", "prod", "strict"; anything else
// is Prod.
func ParseDetail(s string) Detail {
	switch s {
	case "dev":
		return Dev
	case "strict":
		return Strict
	}
	return Prod
}

// Classified is the result of Classify.
type Classified struct {
	Class Class
	// SQLState is the five-character code, "" for non-PG errors.
	SQLState string
	// Message is the client-facing text after the Detail policy.
	Message string
	// PG is the underlying error for dev-mode extensions (detail, hint,
	// table, constraint); nil for non-PG errors.
	PG *pgconn.PgError
}

// Classify categorises err and renders its client-facing message.
func Classify(err error, detail Detail) Classified {
	if err == nil {
		return Classified{Class: OK}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		c := Classified{Class: classOf(pgErr.Code), SQLState: pgErr.Code, PG: pgErr}
		if detail == Dev {
			c.Message = pgErr.Message
		} else {
			c.Message = SanitizeMessage(pgErr, detail == Strict)
		}
		return c
	}
	c := Classified{Class: Internal, Message: "internal error"}
	var connErr *pgconn.ConnectError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		c.Class = Timeout
	case errors.As(err, &connErr):
		c.Class = Resource
	}
	if detail == Dev {
		c.Message = err.Error()
	}
	return c
}

func classOf(code string) Class {
	switch code {
	case "42501":
		return Permission
	case "23505", "23503", "23P01", "40001", "40P01":
		return Conflict
	case "23502", "23514":
		return UserInput
	case "57014":
		return Timeout
	case "57P01", "57P02", "57P03":
		return Resource
	}
	switch prefix(code) {
	case "22", "42":
		return UserInput
	case "53", "08":
		return Resource
	}
	return Internal
}

// SanitizeMessage keeps actionable constraint-class errors and hides the
// rest behind a generic message. strict hides those too: PG constraint
// messages embed constraint/table/column names, which strict mode treats as
// sensitive.
func SanitizeMessage(pgErr *pgconn.PgError, strict bool) string {
	switch prefix(pgErr.Code) {
	case "23": // integrity constraint violations are user-actionable
		if strict {
			return "constraint violation"
		}
		return pgErr.Message
	case "22": // data exceptions (bad input format)
		if strict {
			return "invalid input value"
		}
		return pgErr.Message
	case "42": // syntax/authorization: could leak schema internals
		if pgErr.Code == "42501" {
			return "permission denied"
		}
		return "invalid operation"
	default:
		return "database error"
	}
}

func prefix(code string) string {
	if len(code) < 2 {
		return strings.Repeat("?", 2)
	}
	return code[:2]
}
