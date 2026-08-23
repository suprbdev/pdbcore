// Package inflect defines the naming pipeline shared by every product: an
// open Kind, the Input a naming hook receives, the Next continuation, and the
// pure string helpers (camel casing, singular/plural, enum value names). Each
// product declares its own Kind constants and default inflector on top.
package inflect

import (
	"strings"
	"unicode"
)

// Kind identifies which name is being generated. It is an open string type:
// each product defines its constants, so InflectionHook plugins compile
// against this package alone.
type Kind string

// Input carries everything a hook may need to build a name.
type Input struct {
	Schema     string   // pg schema name
	Table      string   // pg table/view name
	Column     string   // pg column name (when applicable)
	Columns    []string // multi-column keys
	Constraint string   // pg constraint name backing the name (relation kinds)
	Function   string   // pg function name (when applicable)
	Enum       string   // pg enum type name
	Value      string   // enum value
	IsList     bool
}

// Next continues the chain; the last Next is the product's default inflector.
type Next func(kind Kind, in Input) string

// UpperCamel converts snake_case to UpperCamelCase.
func UpperCamel(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == ' ':
			up = true
		case up:
			b.WriteRune(unicode.ToUpper(r))
			up = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LowerCamel converts snake_case to lowerCamelCase.
func LowerCamel(s string) string {
	c := UpperCamel(s)
	if c == "" {
		return c
	}
	return strings.ToLower(c[:1]) + c[1:]
}

// EnumValue converts a Postgres enum label to an UPPER_SNAKE enum value name.
func EnumValue(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case unicode.IsLetter(r):
			if unicode.IsUpper(r) && i > 0 && b.Len() > 0 {
				prev := rune(s[i-1])
				if unicode.IsLower(prev) {
					b.WriteRune('_')
				}
			}
			b.WriteRune(unicode.ToUpper(r))
		case unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	if unicode.IsDigit(rune(out[0])) {
		out = "_" + out
	}
	return out
}

// ByColumns joins UpperCamel column names with "And" (the "ById",
// "ByEmailAndTenant" suffix used by key-based names).
func ByColumns(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = UpperCamel(c)
	}
	return strings.Join(parts, "And")
}

// Irregular noun forms, matched on the trailing word of snake_case names so
// organisation_person -> organisation_people. Plugins can override the rest.
var irregularPlurals = map[string]string{
	"person": "people",
	"child":  "children",
}

var irregularSingulars = func() map[string]string {
	out := make(map[string]string, len(irregularPlurals))
	for s, p := range irregularPlurals {
		out[p] = s
	}
	return out
}()

// irregular replaces the trailing word of s using the given form map, or
// returns "" when no irregular applies.
func irregular(s string, forms map[string]string) string {
	for from, to := range forms {
		if s == from {
			return to
		}
		if strings.HasSuffix(s, "_"+from) {
			return s[:len(s)-len(from)] + to
		}
	}
	return ""
}

// Singularize applies simple English singularization rules — enough for
// common table names; plugins can override anything it gets wrong.
func Singularize(s string) string {
	if out := irregular(s, irregularSingulars); out != "" {
		return out
	}
	if irregular(s, irregularPlurals) != "" {
		return s // already singular (person stays person, not perso)
	}
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses") || strings.HasSuffix(s, "xes") || strings.HasSuffix(s, "zes") || strings.HasSuffix(s, "ches") || strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return s[:len(s)-1]
	}
	return s
}

// Pluralize applies simple English pluralization rules.
func Pluralize(s string) string {
	if out := irregular(s, irregularPlurals); out != "" {
		return out
	}
	if irregular(s, irregularSingulars) != "" {
		return s // already plural (people stays people)
	}
	switch {
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(s[len(s)-2]):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s") || strings.HasSuffix(s, "x") || strings.HasSuffix(s, "z") || strings.HasSuffix(s, "ch") || strings.HasSuffix(s, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}
