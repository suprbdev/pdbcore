package introspect

import (
	"fmt"
	"strings"
)

// MissingArgsError reports input parameters that a call has to pass but
// left out: see [Function.CallSQL].
type MissingArgsError struct {
	// Function is the function's "schema.name".
	Function string
	// Args are the parameter names in declaration order.
	Args []string
}

func (e *MissingArgsError) Error() string {
	return fmt.Sprintf("function %s: missing required argument(s): %s", e.Function, strings.Join(e.Args, ", "))
}

// CallSQL renders an invocation of f that passes only the parameters the
// caller supplies, e.g.
//
//	"public"."f"("a" => $1, "c" => $2)
//
// arg is called once per input parameter (f.Args) in declaration order and
// returns the SQL expression to pass, typically a placeholder from the
// caller's own parameter allocator, optionally with a cast. Returning
// ok=false leaves the parameter out so PostgreSQL evaluates its DEFAULT;
// pass "NULL" explicitly to pass a null. Allocating placeholders inside arg,
// only for the parameters it passes, numbers them in declaration order, and
// the SQL text then depends only on which parameters are passed, never on
// their values.
//
// Calls use named notation, which can leave out any parameter with a
// default. A variadic function is the exception: PostgreSQL matches named
// notation to it only when every parameter is passed and the VARIADIC one
// is labeled, so it is called in positional notation, which can only leave
// out trailing parameters:
//
//	"public"."g"($1, VARIADIC $2)
//
// The VARIADIC parameter takes an array expression and is rendered with the
// VARIADIC keyword; pass an empty array for zero elements.
//
// An error returned by arg is returned as is. Leaving out a parameter that
// has no default, or that precedes a passed parameter of a variadic
// function, yields a *MissingArgsError naming all such parameters.
func (f *Function) CallSQL(arg func(a FuncArg) (expr string, ok bool, err error)) (string, error) {
	positional := f.Variadic() != nil
	exprs := make([]string, len(f.Args))
	passed := make([]bool, len(f.Args))
	last := -1
	for i, a := range f.Args {
		expr, ok, err := arg(a)
		if err != nil {
			return "", err
		}
		if ok {
			exprs[i], passed[i], last = expr, true, i
		}
	}
	var parts, missing []string
	for i, a := range f.Args {
		if !passed[i] {
			if !a.HasDefault || (positional && i < last) {
				missing = append(missing, a.Name)
			}
			continue
		}
		p := exprs[i]
		if !positional {
			p = quoteIdent(a.Name) + " => " + p
		}
		if a.Variadic() {
			p = "VARIADIC " + p
		}
		parts = append(parts, p)
	}
	if len(missing) > 0 {
		return "", &MissingArgsError{Function: f.Schema + "." + f.Name, Args: missing}
	}
	return quoteIdent(f.Schema) + "." + quoteIdent(f.Name) + "(" + strings.Join(parts, ", ") + ")", nil
}
