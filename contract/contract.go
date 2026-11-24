// Package contract derives schema contracts: named, audience-specific views
// of one catalog (public, partner, internal, ...) that each product builds
// its own API from, served side by side. Membership comes from smart
// comments, parsed by [smarttags.Tags.Contracts]:
//
//	COMMENT ON TABLE app.audit_log IS '@contract internal';
//	COMMENT ON COLUMN app.users.email IS '@behavior -contract:public';
//	COMMENT ON FUNCTION app.search(text) IS '@behavior +contract:partner';
//
// A contract only ever removes surface. [Contract.View] drops the tables,
// functions and foreign keys the contract hides from the catalog and marks
// hidden columns with a bare @omit, so the products' own @omit handling
// (which already covers every surface a column reaches: output, filters,
// ordering, aggregates, keys, inputs) hides them; nothing is renamed,
// granted or added. Row-level security is untouched: a contract narrows
// the API, the database still decides which rows a role sees.
//
// Membership rules, for contract name and object o:
//
//   - -contract:name in o's @behavior hides o.
//   - Otherwise o is in the contract when @contract lists name or
//     +contract:name (also contract:name) is in its @behavior.
//   - Otherwise a @contract tag naming only other contracts (or none:
//     a bare @contract) hides o.
//   - Otherwise tables and functions follow the contract's [Policy];
//     columns and foreign keys follow their table: a column or relation of
//     a visible table is visible unless its own tags hide it.
//
// A partition follows its parent table like a column does. Hiding a table
// also hides its partitions, its foreign keys and every
// foreign key referencing it, every function taking or returning its row
// type, and every column typed as its row type.
package contract

import (
	"fmt"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
)

// Policy is what a contract does with a table or function whose tags say
// nothing about it.
type Policy string

// The contract policies.
const (
	// Include shows untagged tables and functions: the contract is the
	// full API minus what is tagged out of it.
	Include Policy = "include"
	// Exclude hides untagged tables and functions: the contract is only
	// what is tagged into it.
	Exclude Policy = "exclude"
)

// ParsePolicy maps the config spelling of a policy ("include", "exclude",
// or "" for [Include]) to its Policy. Any other value is an error naming
// the accepted ones.
func ParsePolicy(s string) (Policy, error) {
	switch Policy(s) {
	case "", Include:
		return Include, nil
	case Exclude:
		return Exclude, nil
	}
	return "", fmt.Errorf("%q is not 'include' or 'exclude'", s)
}

// Contract is one named view of the catalog.
type Contract struct {
	// Name is matched case-insensitively against the names in @contract
	// and @behavior tags.
	Name string
	// Default is the policy for untagged tables and functions; empty
	// means [Include].
	Default Policy
}

// Summary counts what [Contract.View] hid.
type Summary struct {
	Tables, Columns, Functions, ForeignKeys int
}

// View returns a deep copy of c without everything the contract hides
// (see the package documentation), and what it hid; c is not modified.
// Columns are not removed, since keys, indexes and foreign keys keep
// naming them: a hidden column's comment gets a leading bare @omit tag
// line instead. A Default other than "", [Include] or [Exclude] is an
// error.
func (ct Contract) View(c *introspect.Catalog) (*introspect.Catalog, Summary, error) {
	policy, err := ParsePolicy(string(ct.Default))
	if err != nil {
		return nil, Summary{}, fmt.Errorf("contract %s: default: %w", ct.Name, err)
	}
	cp := c.Clone()
	return cp, apply(cp, strings.ToLower(ct.Name), policy == Include), nil
}

// apply removes from c, in place, everything contract name hides, given
// def, whether untagged tables and functions are in the contract, and
// reindexes c.
func apply(c *introspect.Catalog, name string, def bool) Summary {
	var sum Summary
	in := func(comment string, def bool) bool {
		if !strings.Contains(comment, "@") {
			return def
		}
		tags, _ := smarttags.Parse(comment)
		return tags.Contracts().In(name, def)
	}

	byRef := make(map[string]*introspect.Table, len(c.Tables))
	for _, t := range c.Tables {
		byRef[t.Schema+"."+t.Name] = t
	}
	visible := make(map[*introspect.Table]bool, len(c.Tables))
	var decide func(t *introspect.Table) bool
	decide = func(t *introspect.Table) bool {
		if v, ok := visible[t]; ok {
			return v
		}
		visible[t] = false
		v := in(t.Comment, def)
		if parent := byRef[t.PartitionOf]; parent != nil {
			pv := decide(parent)
			v = pv && in(t.Comment, pv)
		}
		visible[t] = v
		return v
	}
	hidden := map[string]bool{}
	tables := make([]*introspect.Table, 0, len(c.Tables))
	for _, t := range c.Tables {
		if decide(t) {
			tables = append(tables, t)
		} else {
			hidden[t.Schema+"."+t.Name] = true
			c.HideRelation(t.Schema, t.Name)
			sum.Tables++
		}
	}
	c.Tables = tables
	rowOfHidden := func(schema, typ string) bool {
		return len(hidden) > 0 && (hidden[schema+"."+typ] ||
			strings.HasPrefix(typ, "_") && hidden[schema+"."+typ[1:]])
	}

	for _, t := range c.Tables {
		for _, col := range t.Columns {
			if !in(col.Comment, true) || rowOfHidden(col.TypeSchema, col.PGType) {
				col.Comment = omitAll(col.Comment)
				sum.Columns++
			}
		}
		fks := t.ForeignKeys[:0]
		for _, fk := range t.ForeignKeys {
			if hidden[fk.RefSchema+"."+fk.RefTable] || !in(fk.Comment, true) {
				sum.ForeignKeys++
				continue
			}
			fks = append(fks, fk)
		}
		clear(t.ForeignKeys[len(fks):])
		t.ForeignKeys = fks
	}

	functions := make([]*introspect.Function, 0, len(c.Functions))
	for _, f := range c.Functions {
		if !in(f.Comment, def) || functionUsesHidden(f, rowOfHidden) {
			sum.Functions++
			continue
		}
		functions = append(functions, f)
	}
	c.Functions = functions
	c.Reindex()
	return sum
}

func functionUsesHidden(f *introspect.Function, rowOfHidden func(schema, typ string) bool) bool {
	if rowOfHidden(f.ReturnTypeSchema, f.ReturnType) {
		return true
	}
	for _, a := range f.Args {
		if rowOfHidden(a.TypeSchema, a.PGType) {
			return true
		}
	}
	for _, a := range f.ReturnColumns {
		if rowOfHidden(a.TypeSchema, a.PGType) {
			return true
		}
	}
	return false
}

// omitAll returns comment with a bare @omit tag line in front: the object
// is hidden from every surface, and its other tags and description stay
// as they were.
func omitAll(comment string) string {
	if comment == "" {
		return "@omit"
	}
	return "@omit\n" + comment
}
