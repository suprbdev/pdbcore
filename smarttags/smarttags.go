// Package smarttags parses PostGraphile-style "smart comments": lines at the
// start of a database COMMENT beginning with '@' are machine-readable tags,
// everything after the first non-tag line is the human description.
//
//	COMMENT ON TABLE users IS E'@name people\n@omit delete\nA user of the app.';
//
// parses to {name: ["people"], omit: ["delete"]} plus "A user of the app.".
// The parser is shared by the smart-comments and advanced-filters plugins so
// both read the same syntax from the same catalog comments.
package smarttags

import "strings"

// Tags maps a tag name to its values, one entry per occurrence in the
// comment; a bare tag (no value) contributes an empty string. A nil Tags is
// valid and empty.
type Tags map[string][]string

// Parse splits a comment into its smart tags and the remaining description.
// Comments with no leading '@' line are returned unchanged with nil tags.
func Parse(comment string) (Tags, string) {
	if !strings.HasPrefix(strings.TrimSpace(comment), "@") {
		return nil, comment
	}
	lines := strings.Split(comment, "\n")
	tags := Tags{}
	i := 0
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue // blank lines between tags don't end the tag block
		}
		if !strings.HasPrefix(line, "@") {
			break
		}
		name, value := line[1:], ""
		if cut := strings.IndexAny(name, " \t"); cut >= 0 {
			name, value = name[:cut], strings.TrimSpace(name[cut+1:])
		}
		if name != "" {
			tags[name] = append(tags[name], value)
		}
	}
	return tags, strings.TrimSpace(strings.Join(lines[i:], "\n"))
}

// Strip returns the comment with any leading smart-tag lines removed.
func Strip(comment string) string {
	_, desc := Parse(comment)
	return desc
}

// Has reports whether the tag occurs at least once.
func (t Tags) Has(name string) bool {
	_, ok := t[name]
	return ok
}

// First returns the first non-empty value of the tag, or "".
func (t Tags) First(name string) string {
	for _, v := range t[name] {
		if v != "" {
			return v
		}
	}
	return ""
}

// All returns every value of the tag in order (nil when absent).
func (t Tags) All(name string) []string { return t[name] }

// Omits collects the actions named across all @omit occurrences (lowercased,
// split on commas and whitespace: "@omit create, update"). A bare @omit sets
// everything instead: the object is omitted entirely.
//
// PostGraphile-V5-style @behavior tags contribute too: their negative tokens
// map onto omit actions (-insert -> create, -update -> update, -delete ->
// delete, -select -> read, -filterBy -> filter, -orderBy -> order, matched
// case-insensitively). The deprecated -filter and -order still map to
// filter and order (see [Tags.Check]); other tokens are ignored.
func (t Tags) Omits() (actions map[string]bool, everything bool) {
	actions = map[string]bool{}
	for _, v := range t["omit"] {
		if v == "" {
			everything = true
			continue
		}
		for _, a := range SplitList(v) {
			actions[strings.ToLower(a)] = true
		}
	}
	for _, v := range t["behavior"] {
		for _, tok := range SplitList(v) {
			if action, ok := behaviorOmits[strings.ToLower(tok)]; ok {
				actions[action] = true
			}
		}
	}
	return actions, everything
}

// behaviorOmits maps negative @behavior tokens, lowercased, to @omit
// actions.
var behaviorOmits = map[string]string{
	"-insert":   "create",
	"-update":   "update",
	"-delete":   "delete",
	"-select":   "read",
	"-filterby": "filter",
	"-orderby":  "order",
	"-filter":   "filter",
	"-order":    "order",
}

// deprecatedBehaviors maps deprecated @behavior tokens, lowercased, to the
// PostGraphile V5 token replacing them.
var deprecatedBehaviors = map[string]string{
	"-filter": "-filterBy",
	"-order":  "-orderBy",
}

// Vocabulary is the set of @omit actions and @behavior tokens a product
// honours, against which [Tags.Check] reports unknown ones. The omit
// tokens of @behavior (see [Tags.Omits]) and the contract tokens (see
// [Tags.Contracts]) are always known.
type Vocabulary struct {
	// OmitActions lists the @omit actions honoured, matched
	// case-insensitively.
	OmitActions []string
	// Behavior reports whether a product-specific @behavior token is
	// honoured; it receives the token as written, sign included. Nil knows
	// no product-specific tokens.
	Behavior func(token string) bool
}

// IssueKind classifies an [Issue].
type IssueKind int

// The issue kinds.
const (
	// UnknownOmit is an @omit action outside [Vocabulary.OmitActions].
	UnknownOmit IssueKind = iota + 1
	// UnknownBehavior is an @behavior token nothing honours.
	UnknownBehavior
	// DeprecatedBehavior is an @behavior token still honoured under an old
	// spelling; [Issue.Use] names the replacement.
	DeprecatedBehavior
)

// Issue is a token [Tags.Check] reports.
type Issue struct {
	Kind IssueKind
	// Token is the @omit action or @behavior token as written.
	Token string
	// Use is the replacement of a [DeprecatedBehavior] token, "" otherwise.
	Use string
}

// Message is a short description of the issue for a log line:
// "unknown @omit action ignored", "unknown @behavior token ignored" or
// "deprecated @behavior token".
func (i Issue) Message() string {
	switch i.Kind {
	case UnknownOmit:
		return "unknown @omit action ignored"
	case UnknownBehavior:
		return "unknown @behavior token ignored"
	case DeprecatedBehavior:
		return "deprecated @behavior token"
	}
	return "smart tag issue"
}

// Attrs returns the issue's details as slog key-value pairs: "action" for
// an @omit action, "token" (and "use" for a deprecated one) for an
// @behavior token.
func (i Issue) Attrs() []any {
	switch i.Kind {
	case UnknownOmit:
		return []any{"action", i.Token}
	case DeprecatedBehavior:
		return []any{"token", i.Token, "use", i.Use}
	}
	return []any{"token", i.Token}
}

// Check reports the unknown @omit actions, unknown @behavior tokens and
// deprecated @behavior tokens of t against v, in tag order and once per
// distinct token.
func (t Tags) Check(v Vocabulary) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	add := func(key string, i Issue) {
		if !seen[key] {
			seen[key] = true
			issues = append(issues, i)
		}
	}
	for _, val := range t["omit"] {
		for _, a := range SplitList(val) {
			if !containsFold(v.OmitActions, a) {
				add("omit "+a, Issue{Kind: UnknownOmit, Token: a})
			}
		}
	}
	for _, val := range t["behavior"] {
		for _, tok := range SplitList(val) {
			if use := deprecatedBehaviors[strings.ToLower(tok)]; use != "" {
				add("behavior "+tok, Issue{Kind: DeprecatedBehavior, Token: tok, Use: use})
			} else if !v.knows(tok) {
				add("behavior "+tok, Issue{Kind: UnknownBehavior, Token: tok})
			}
		}
	}
	return issues
}

// knows reports whether the @behavior token tok is honoured: an omit or
// contract token, or one v.Behavior accepts.
func (v Vocabulary) knows(tok string) bool {
	if _, ok := behaviorOmits[strings.ToLower(tok)]; ok || isContractToken(tok) {
		return true
	}
	return v.Behavior != nil && v.Behavior(tok)
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// isContractToken reports whether tok is +contract:<name>,
// -contract:<name> or contract:<name>.
func isContractToken(tok string) bool {
	_, name, ok := contractToken(tok)
	return ok && name != ""
}

// contractToken splits a contract @behavior token into its sign ('+', '-'
// or 0) and lowercased contract name.
func contractToken(tok string) (sign byte, name string, ok bool) {
	rest := tok
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		sign, rest = rest[0], rest[1:]
	}
	name, ok = strings.CutPrefix(strings.ToLower(rest), "contract:")
	return sign, name, ok
}

// Membership is an object's schema-contract membership, read by
// [Tags.Contracts] from the @contract tag and the contract tokens of
// @behavior. Contract names are lowercased.
type Membership struct {
	// Listed reports that the object carries at least one @contract tag:
	// it belongs to the contracts in Only (plus Include) and to no other.
	Listed bool
	// Only holds the names listed by @contract (@contract public, partner).
	Only map[string]bool
	// Include holds the names of +contract:<name> (or contract:<name>)
	// @behavior tokens.
	Include map[string]bool
	// Exclude holds the names of -contract:<name> @behavior tokens.
	Exclude map[string]bool
}

// Contracts collects the object's contract membership: the names listed
// across every @contract occurrence (split on commas and whitespace, so
// "@contract public, partner") and the @behavior tokens +contract:<name>,
// contract:<name> and -contract:<name>. A bare @contract lists no names:
// the object belongs to no contract unless a +contract token includes it.
func (t Tags) Contracts() Membership {
	var m Membership
	add := func(set *map[string]bool, name string) {
		if *set == nil {
			*set = map[string]bool{}
		}
		(*set)[strings.ToLower(name)] = true
	}
	for _, v := range t["contract"] {
		m.Listed = true
		for _, name := range SplitList(v) {
			add(&m.Only, name)
		}
	}
	for _, v := range t["behavior"] {
		for _, tok := range SplitList(v) {
			sign, name, ok := contractToken(tok)
			if !ok || name == "" {
				continue
			}
			if sign == '-' {
				add(&m.Exclude, name)
			} else {
				add(&m.Include, name)
			}
		}
	}
	return m
}

// In reports whether the object belongs to contract name, given def, the
// answer when the tags say nothing about it: the parent object's
// membership for a column or relation, the contract's default policy for
// a table or function.
// -contract:<name> wins over everything, then @contract and +contract
// include, then a @contract list naming other contracts excludes.
func (m Membership) In(name string, def bool) bool {
	switch {
	case m.Exclude[name]:
		return false
	case m.Only[name] || m.Include[name]:
		return true
	case m.Listed:
		return false
	}
	return def
}

// SplitList splits a tag value on commas and whitespace, dropping empties —
// the list form shared by @omit, @primaryKey, and @unique values.
func SplitList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
}
