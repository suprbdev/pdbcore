package introspect

import "slices"

// Clone returns a deep copy of c: every table, column, constraint, index,
// enum, composite, function, extension, range and domain is copied, so
// catalog hooks can mutate the copy (one per schema contract, for example)
// without affecting c or each other. The copy's lookup indexes start empty
// and build on first use.
func (c *Catalog) Clone() *Catalog {
	if c == nil {
		return nil
	}
	return &Catalog{
		FormatVersion:    c.FormatVersion,
		ServerVersion:    c.ServerVersion,
		ServerVersionNum: c.ServerVersionNum,
		Schemas:          slices.Clone(c.Schemas),
		Tables:           clonePtrs(c.Tables, (*Table).clone),
		Enums:            clonePtrs(c.Enums, (*Enum).clone),
		Composites:       clonePtrs(c.Composites, (*Composite).clone),
		Functions:        clonePtrs(c.Functions, (*Function).clone),
		Extensions:       clonePtrs(c.Extensions, func(e *Extension) *Extension { cp := *e; return &cp }),
		Ranges:           clonePtrs(c.Ranges, func(r *RangeType) *RangeType { cp := *r; return &cp }),
		Domains:          clonePtrs(c.Domains, (*Domain).clone),
	}
}

func clonePtrs[T any](s []*T, clone func(*T) *T) []*T {
	if s == nil {
		return nil
	}
	out := make([]*T, len(s))
	for i, e := range s {
		if e != nil {
			out[i] = clone(e)
		}
	}
	return out
}

func (t *Table) clone() *Table {
	cp := *t
	cp.Columns = clonePtrs(t.Columns, (*Column).clone)
	if t.PrimaryKey != nil {
		cp.PrimaryKey = t.PrimaryKey.clone()
	}
	cp.Uniques = clonePtrs(t.Uniques, (*Constraint).clone)
	cp.ForeignKeys = clonePtrs(t.ForeignKeys, (*ForeignKey).clone)
	cp.Indexes = clonePtrs(t.Indexes, (*Index).clone)
	return &cp
}

func (c *Column) clone() *Column {
	cp := *c
	if c.Spatial != nil {
		s := *c.Spatial
		cp.Spatial = &s
	}
	return &cp
}

func (c *Constraint) clone() *Constraint {
	cp := *c
	cp.Columns = slices.Clone(c.Columns)
	return &cp
}

func (fk *ForeignKey) clone() *ForeignKey {
	cp := *fk
	cp.Columns = slices.Clone(fk.Columns)
	cp.RefColumns = slices.Clone(fk.RefColumns)
	return &cp
}

func (ix *Index) clone() *Index {
	cp := *ix
	cp.Columns = slices.Clone(ix.Columns)
	return &cp
}

func (e *Enum) clone() *Enum {
	cp := *e
	cp.Values = slices.Clone(e.Values)
	cp.ValueDescriptions = slices.Clone(e.ValueDescriptions)
	return &cp
}

func (ct *Composite) clone() *Composite {
	cp := *ct
	cp.Fields = clonePtrs(ct.Fields, (*Column).clone)
	return &cp
}

func (f *Function) clone() *Function {
	cp := *f
	cp.Args = slices.Clone(f.Args)
	cp.ReturnColumns = slices.Clone(f.ReturnColumns)
	return &cp
}

func (d *Domain) clone() *Domain {
	cp := *d
	cp.Checks = slices.Clone(d.Checks)
	return &cp
}
