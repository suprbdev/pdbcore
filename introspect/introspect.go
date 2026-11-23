package introspect

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Querier is the subset of pgx used by introspection (satisfied by *pgx.Conn,
// pgxpool.Pool, and pgx.Tx).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Introspect reads pg_catalog for the given schemas and builds a Catalog.
// pg_catalog is used instead of information_schema because the latter loses
// index, RLS, and array/domain detail.
func Introspect(ctx context.Context, db Querier, schemas []string) (*Catalog, error) {
	if len(schemas) == 0 {
		schemas = []string{"public"}
	}
	cat := &Catalog{FormatVersion: CatalogFormatVersion, Schemas: schemas}

	if err := db.QueryRow(ctx, `SELECT current_setting('server_version'), current_setting('server_version_num')::int`).
		Scan(&cat.ServerVersion, &cat.ServerVersionNum); err != nil {
		return nil, fmt.Errorf("introspect: server version: %w", err)
	}
	if err := readTables(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readColumns(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readConstraints(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readIndexes(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readEnums(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readComposites(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readFunctions(ctx, db, schemas, cat); err != nil {
		return nil, err
	}
	if err := readExtensions(ctx, db, cat); err != nil {
		return nil, err
	}
	if err := readRanges(ctx, db, cat); err != nil {
		return nil, err
	}
	if err := readDomains(ctx, db, cat); err != nil {
		return nil, err
	}
	if err := convertEnumTables(ctx, db, cat); err != nil {
		return nil, err
	}
	cat.sortStable()
	return cat, nil
}

// readTables reads the tables, views and materialized views of schemas the
// current role can SELECT from, skipping extension members (PostGIS's
// spatial_ref_sys, geometry_columns, raster_columns, ...): like extension
// functions they are implementation detail, and catalog views such as
// geometry_columns list columns of every table, hidden or not.
func readTables(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, c.relkind, c.relrowsecurity,
		       COALESCE(obj_description(c.oid, 'pg_class'), ''),
		       has_table_privilege(c.oid, 'SELECT'),
		       has_table_privilege(c.oid, 'INSERT'),
		       has_table_privilege(c.oid, 'UPDATE'),
		       has_table_privilege(c.oid, 'DELETE'),
		       COALESCE((SELECT pn.nspname || '.' || pc.relname
		                 FROM pg_inherits i
		                 JOIN pg_class pc ON pc.oid = i.inhparent
		                 JOIN pg_namespace pn ON pn.oid = pc.relnamespace
		                 WHERE c.relispartition AND i.inhrelid = c.oid), '')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r', 'p', 'v', 'm')
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid
		                    AND d.deptype = 'e')
		ORDER BY n.nspname, c.relname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		t := &Table{}
		var kind rune
		if err := rows.Scan(&t.Schema, &t.Name, &kind, &t.RLSEnabled, &t.Comment,
			&t.Privileges.Select, &t.Privileges.Insert, &t.Privileges.Update, &t.Privileges.Delete,
			&t.PartitionOf); err != nil {
			return err
		}
		t.Partitioned = kind == 'p'
		switch kind {
		case 'r', 'p':
			t.Kind = RelTable
		case 'v':
			t.Kind = RelView
		case 'm':
			t.Kind = RelMaterializedView
		}
		if t.Privileges.Select {
			cat.Tables = append(cat.Tables, t)
		}
	}
	return rows.Err()
}

// typmodColumns selects a column's modifier for applyTypmod: its
// format_type text (empty when it has none), whether the base type is built
// in, and whether the base type (or its array element) is PostGIS geometry
// or geography in the postgis extension's own schema. It expects the lateral
// m from resolvedType. format_type is core and dispatches to the type's own
// typmodout function, so this works with or without PostGIS installed and
// never depends on PostGIS's internal typmod bit layout.
const typmodColumns = `CASE WHEN m.typmod >= 0 THEN format_type(m.typoid, m.typmod) ELSE '' END,
		       m.typnamespace = 'pg_catalog'::regnamespace,
		       EXISTS (SELECT 1 FROM pg_type st
		               JOIN pg_extension se ON se.extname = 'postgis' AND se.extnamespace = st.typnamespace
		               WHERE st.oid = CASE WHEN m.typcategory = 'A' THEN m.typelem ELSE m.typoid END
		                 AND st.typname IN ('geometry', 'geography'))`

// resolvedType joins lateral m: the pg_type alias typ resolved through any
// chain of domains to the first non-domain base (typoid, typname,
// typnamespace, typcategory, typelem), plus the effective modifier typmod:
// a.atttypmod for a non-domain type, otherwise the typtypmod of the nearest
// domain in the chain that declares one (-1 if none does). An array of a
// domain resolves its element domain instead and yields the array type of
// that domain's base (so email[] becomes _text), unless the base has no
// array type (a domain over an array), which leaves typ as it is. domain is
// the "schema.name" of the domain typ is, or of the element domain so
// resolved; empty otherwise.
func resolvedType(typ string) string {
	return fmt.Sprintf(`CROSS JOIN LATERAL (
		    WITH RECURSIVE subject AS (
		        SELECT COALESCE(e.oid, %[1]s.oid) AS oid, e.oid IS NOT NULL AS array_of
		        FROM (SELECT 1) one
		        LEFT JOIN pg_type e ON %[1]s.typtype <> 'd' AND %[1]s.typcategory = 'A'
		                           AND e.oid = %[1]s.typelem AND e.typtype = 'd'
		    ), chain AS (
		        SELECT d.oid, d.typtype, d.typbasetype, d.typtypmod, 0 AS depth
		        FROM subject JOIN pg_type d ON d.oid = subject.oid
		        UNION ALL
		        SELECT d.oid, d.typtype, d.typbasetype, d.typtypmod, chain.depth + 1
		        FROM chain JOIN pg_type d ON d.oid = chain.typbasetype
		        WHERE chain.typtype = 'd'
		    ), base AS (
		        SELECT b.oid, b.typarray, subject.array_of AND b.typarray <> 0 AS array_of
		        FROM chain JOIN pg_type b ON b.oid = chain.oid CROSS JOIN subject
		        WHERE chain.typtype <> 'd'
		    )
		    SELECT r.oid AS typoid, r.typname, r.typnamespace, r.typcategory, r.typelem,
		           CASE WHEN %[1]s.typtype = 'd' OR base.array_of
		                THEN COALESCE((SELECT typtypmod FROM chain
		                               WHERE typtype = 'd' AND typtypmod >= 0
		                               ORDER BY depth LIMIT 1), -1)
		                ELSE a.atttypmod END AS typmod,
		           CASE WHEN %[1]s.typtype = 'd' OR base.array_of
		                THEN (SELECT dn.nspname || '.' || dt.typname
		                      FROM subject JOIN pg_type dt ON dt.oid = subject.oid
		                      JOIN pg_namespace dn ON dn.oid = dt.typnamespace)
		                ELSE '' END AS domain
		    FROM base
		    JOIN pg_type r ON r.oid = CASE WHEN base.array_of THEN base.typarray
		                                   WHEN (SELECT array_of FROM subject) THEN %[1]s.oid
		                                   ELSE base.oid END
		) m`, typ)
}

func readColumns(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, a.attname, a.attnum,
		       tn.nspname,
		       m.typname,
		       bn.nspname,
		       m.typcategory = 'A',
		       `+notNullColumn(cat.ServerVersionNum)+`,
		       a.atthasdef,
		       a.attidentity <> '' OR a.attgenerated <> '',
		       CASE WHEN a.attidentity <> '' THEN 'identity'
		            WHEN a.attgenerated = 's' THEN 'stored'
		            WHEN a.attgenerated = 'v' THEN 'virtual'
		            ELSE '' END,
		       COALESCE(col_description(c.oid, a.attnum), ''),
		       m.domain,
		       `+typmodColumns+`
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON t.oid = a.atttypid
		JOIN pg_namespace tn ON tn.oid = t.typnamespace
		`+resolvedType("t")+`
		JOIN pg_namespace bn ON bn.oid = m.typnamespace
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r', 'p', 'v', 'm')
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY n.nspname, c.relname, a.attnum`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, genKind, formatted string
		var builtin, spatial bool
		col := &Column{}
		if err := rows.Scan(&schema, &table, &col.Name, &col.Position,
			&col.TypeSchema, &col.PGType, &col.BaseTypeSchema, &col.IsArray,
			&col.NotNull, &col.HasDefault, &col.Generated, &genKind, &col.Comment,
			&col.Domain, &formatted, &builtin, &spatial); err != nil {
			return err
		}
		col.GeneratedKind = GeneratedKind(genKind)
		applyTypmod(col, formatted, builtin, spatial)
		if t := cat.Table(schema, table); t != nil {
			t.Columns = append(t.Columns, col)
		}
	}
	return rows.Err()
}

// notNullColumn selects whether pg_attribute a holds no NULL. From
// PostgreSQL 18 a NOT NULL constraint added NOT VALID sets attnotnull while
// rows predating it may still hold NULL, so attnotnull counts only when the
// column's NOT NULL constraint (contype 'n') is validated.
func notNullColumn(serverVersionNum int) string {
	if serverVersionNum >= 180000 {
		return `a.attnotnull AND NOT EXISTS (SELECT 1 FROM pg_constraint nn
		               WHERE nn.conrelid = a.attrelid AND nn.contype = 'n'
		                 AND nn.conkey[1] = a.attnum AND NOT nn.convalidated)`
	}
	return `a.attnotnull`
}

// pg18ConstraintColumns selects (conperiod, conenforced), which exist only
// from PostgreSQL 18; older servers get the values every constraint had there.
func pg18ConstraintColumns(serverVersionNum int) string {
	if serverVersionNum >= 180000 {
		return `con.conperiod, con.conenforced`
	}
	return `false, true`
}

// readConstraints reads primary key, unique, foreign key and check
// constraints, skipping the copies PostgreSQL clones from a partitioned
// table's constraints (conparentid <> 0): one per partition, plus, for a
// foreign key referencing a partitioned table, one per referenced partition
// on the referencing table. Check constraints are inherited rather than
// cloned, so a partition's are skipped unless declared on it (conislocal).
// A check's columns are the ones its expression references (conkey),
// where 0 stands for a whole-row reference and selects every column.
func readConstraints(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, con.conname, con.contype,
		       CASE WHEN con.contype = 'c'
		            THEN ARRAY(SELECT a.attname FROM pg_attribute a
		                       WHERE a.attrelid = con.conrelid AND a.attnum > 0 AND NOT a.attisdropped
		                         AND (a.attnum = ANY(con.conkey) OR 0 = ANY(con.conkey))
		                       ORDER BY a.attnum)
		            ELSE ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
		                       JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		                       ORDER BY k.ord) END,
		       COALESCE(fn.nspname, ''), COALESCE(fc.relname, ''),
		       COALESCE(ARRAY(SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
		             ORDER BY k.ord), '{}'),
		       COALESCE(obj_description(con.oid, 'pg_constraint'), ''),
		       con.convalidated,
		       `+pg18ConstraintColumns(cat.ServerVersionNum)+`
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_class fc ON fc.oid = con.confrelid
		LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		WHERE n.nspname = ANY($1) AND con.contype IN ('p', 'u', 'f', 'c')
		  AND con.conparentid = 0
		  AND (con.contype <> 'c' OR con.conislocal OR NOT c.relispartition)
		ORDER BY n.nspname, c.relname, con.conname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: constraints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, name, comment string
		var ctype rune
		var cols, refCols []string
		var refSchema, refTable string
		var validated, period, enforced bool
		if err := rows.Scan(&schema, &table, &name, &ctype, &cols, &refSchema, &refTable, &refCols, &comment,
			&validated, &period, &enforced); err != nil {
			return err
		}
		t := cat.Table(schema, table)
		if t == nil {
			continue
		}
		switch ctype {
		case 'p':
			t.PrimaryKey = &Constraint{Name: name, Columns: cols, Comment: comment, WithoutOverlaps: period}
		case 'u':
			t.Uniques = append(t.Uniques, &Constraint{Name: name, Columns: cols, Comment: comment, WithoutOverlaps: period})
		case 'f':
			t.ForeignKeys = append(t.ForeignKeys, &ForeignKey{
				Name: name, Columns: cols,
				RefSchema: refSchema, RefTable: refTable, RefColumns: refCols,
				Comment: comment, Period: period, NotEnforced: !enforced,
				NotValid: enforced && !validated,
			})
		case 'c':
			t.Checks = append(t.Checks, &Constraint{Name: name, Columns: cols, Comment: comment})
		}
	}
	return rows.Err()
}

// varAttnoPattern matches a Var node in the text form of a pg_node_tree
// (nodeToString), capturing its varattno. Every PostgreSQL release from 14
// on writes varno and varattno as a Var's first two fields. Constants are
// written as bytes, never as text, so the pattern cannot match inside a
// literal.
const varAttnoPattern = `\{VAR :varno \d+ :varattno (\d+) `

// indexKeys is the FROM item over every column of pg_index i, one row
// k(attnum, opclass, ord) per column: attnum 0 for an expression, opclass
// NULL for an INCLUDE column (indclass covers the key columns only).
const indexKeys = `unnest(i.indkey::int2[], i.indclass::oid[]) WITH ORDINALITY AS k(attnum, opclass, ord)`

// readIndexes reads valid indexes, skipping partition indexes attached to a
// partitioned index (relispartition): the parent's partitioned index stands
// for them. The columns of key expressions and of the predicate are pulled
// from the Vars of indexprs and indpred, the way pull_varattnos does; a Var
// with varattno 0 is a whole-row reference and selects every column.
func readIndexes(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, c.relname, ic.relname, i.indisunique, am.amname,
		       ARRAY(SELECT a.attname FROM `+indexKeys+`
		             JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		             WHERE k.attnum > 0 ORDER BY k.ord),
		       ARRAY(SELECT a.attname FROM pg_attribute a
		             WHERE a.attrelid = i.indrelid AND a.attnum > 0 AND NOT a.attisdropped
		               AND (a.attnum = ANY(refs.attnums) OR 0 = ANY(refs.attnums))
		             ORDER BY a.attnum),
		       ARRAY(SELECT COALESCE(oc.opcname, '') FROM `+indexKeys+`
		             LEFT JOIN pg_opclass oc ON oc.oid = k.opclass
		             WHERE k.attnum > 0 ORDER BY k.ord)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_am am ON am.oid = ic.relam
		CROSS JOIN LATERAL (
		    SELECT array_agg(v[1]::int2) AS attnums
		    FROM regexp_matches(COALESCE(i.indexprs::text, '') || ' ' || COALESCE(i.indpred::text, ''), $2, 'g') AS v
		) refs
		WHERE n.nspname = ANY($1) AND i.indisvalid AND NOT ic.relispartition
		ORDER BY n.nspname, c.relname, ic.relname`, schemas, varAttnoPattern)
	if err != nil {
		return fmt.Errorf("introspect: indexes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table string
		ix := &Index{}
		if err := rows.Scan(&schema, &table, &ix.Name, &ix.Unique, &ix.Method, &ix.Columns, &ix.ExpressionColumns, &ix.OpClasses); err != nil {
			return err
		}
		if len(ix.ExpressionColumns) == 0 {
			ix.ExpressionColumns = nil
		}
		if t := cat.Table(schema, table); t != nil {
			t.Indexes = append(t.Indexes, ix)
		}
	}
	return rows.Err()
}

func readExtensions(ctx context.Context, db Querier, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT e.extname, e.extversion, n.nspname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		ORDER BY e.extname`)
	if err != nil {
		return fmt.Errorf("introspect: extensions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e := &Extension{}
		if err := rows.Scan(&e.Name, &e.Version, &e.Schema); err != nil {
			return err
		}
		cat.Extensions = append(cat.Extensions, e)
	}
	return rows.Err()
}

// readRanges reads every range type outside pg_catalog with its subtype
// and, from PostgreSQL 14, its multirange type.
func readRanges(ctx context.Context, db Querier, cat *Catalog) error {
	multi := "NULL::oid"
	if cat.ServerVersionNum >= 140000 {
		multi = "r.rngmultitypid"
	}
	rows, err := db.Query(ctx, `
		SELECT n.nspname, t.typname, st.typname, sn.nspname,
		       COALESCE(mt.typname, ''), COALESCE(mn.nspname, '')
		FROM pg_range r
		JOIN pg_type t ON t.oid = r.rngtypid
		JOIN pg_namespace n ON n.oid = t.typnamespace
		JOIN pg_type st ON st.oid = r.rngsubtype
		JOIN pg_namespace sn ON sn.oid = st.typnamespace
		LEFT JOIN pg_type mt ON mt.oid = `+multi+`
		LEFT JOIN pg_namespace mn ON mn.oid = mt.typnamespace
		WHERE n.nspname <> 'pg_catalog'
		ORDER BY n.nspname, t.typname`)
	if err != nil {
		return fmt.Errorf("introspect: ranges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r := &RangeType{}
		if err := rows.Scan(&r.Schema, &r.Name, &r.Subtype, &r.SubtypeSchema, &r.Multirange, &r.MultirangeSchema); err != nil {
			return err
		}
		cat.Ranges = append(cat.Ranges, r)
	}
	return rows.Err()
}

// readDomains reads every domain and keeps those [Catalog.Domains]
// documents: the ones in the introspected schemas, the ones the recorded
// columns, composite attributes and function parameters and results use,
// and their parent domains.
func readDomains(ctx context.Context, db Querier, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, t.typname,
		       COALESCE((SELECT pn.nspname || '.' || pt.typname
		                 FROM pg_type pt JOIN pg_namespace pn ON pn.oid = pt.typnamespace
		                 WHERE pt.oid = t.typbasetype AND pt.typtype = 'd'), ''),
		       m.typname, bn.nspname, m.typcategory = 'A',
		       t.typnotnull, COALESCE(t.typdefault, ''),
		       ARRAY(SELECT pg_get_constraintdef(con.oid) FROM pg_constraint con
		             WHERE con.contypid = t.oid AND con.contype = 'c' ORDER BY con.conname),
		       COALESCE(obj_description(t.oid, 'pg_type'), '')
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		CROSS JOIN LATERAL (
		    WITH RECURSIVE chain AS (
		        SELECT t.typbasetype AS oid
		        UNION ALL
		        SELECT d.typbasetype FROM chain JOIN pg_type d ON d.oid = chain.oid WHERE d.typtype = 'd'
		    )
		    SELECT b.typname, b.typnamespace, b.typcategory
		    FROM chain JOIN pg_type b ON b.oid = chain.oid
		    WHERE b.typtype <> 'd'
		) m
		JOIN pg_namespace bn ON bn.oid = m.typnamespace
		WHERE t.typtype = 'd'
		ORDER BY n.nspname, t.typname`)
	if err != nil {
		return fmt.Errorf("introspect: domains: %w", err)
	}
	defer rows.Close()
	all := map[string]*Domain{}
	var order []*Domain
	for rows.Next() {
		d := &Domain{}
		if err := rows.Scan(&d.Schema, &d.Name, &d.Parent, &d.BaseType, &d.BaseTypeSchema, &d.IsArrayBase,
			&d.NotNull, &d.Default, &d.Checks, &d.Comment); err != nil {
			return err
		}
		if len(d.Checks) == 0 {
			d.Checks = nil
		}
		all[d.Ref()] = d
		order = append(order, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	keep := map[string]bool{}
	var want func(ref string)
	want = func(ref string) {
		if d := all[ref]; d != nil && !keep[ref] {
			keep[ref] = true
			want(d.Parent)
		}
	}
	for _, d := range order {
		if slices.Contains(cat.Schemas, d.Schema) {
			want(d.Ref())
		}
	}
	for _, t := range cat.Tables {
		for _, c := range t.Columns {
			want(c.Domain)
		}
	}
	for _, ct := range cat.Composites {
		for _, f := range ct.Fields {
			want(f.Domain)
		}
	}
	for _, f := range cat.Functions {
		want(f.ReturnDomain)
		for _, a := range f.Args {
			want(a.Domain)
		}
		for _, a := range f.ReturnColumns {
			want(a.Domain)
		}
	}
	for _, d := range order {
		if keep[d.Ref()] {
			cat.Domains = append(cat.Domains, d)
		}
	}
	return nil
}

func readEnums(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, t.typname,
		       ARRAY(SELECT e.enumlabel FROM pg_enum e WHERE e.enumtypid = t.oid ORDER BY e.enumsortorder),
		       COALESCE(obj_description(t.oid, 'pg_type'), '')
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = ANY($1) AND t.typtype = 'e'
		ORDER BY n.nspname, t.typname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: enums: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e := &Enum{}
		if err := rows.Scan(&e.Schema, &e.Name, &e.Values, &e.Comment); err != nil {
			return err
		}
		cat.Enums = append(cat.Enums, e)
	}
	return rows.Err()
}

// readComposites reads user-defined composite types (CREATE TYPE ... AS).
// relkind = 'c' restricts to standalone composites, excluding the row types
// PostgreSQL creates implicitly for every table and view.
func readComposites(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, t.typname, a.attname, a.attnum,
		       atn.nspname,
		       m.typname,
		       bn.nspname,
		       m.typcategory = 'A',
		       COALESCE(col_description(c.oid, a.attnum), ''),
		       m.domain,
		       `+typmodColumns+`
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		JOIN pg_class c ON c.oid = t.typrelid AND c.relkind = 'c'
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		JOIN pg_type at ON at.oid = a.atttypid
		JOIN pg_namespace atn ON atn.oid = at.typnamespace
		`+resolvedType("at")+`
		JOIN pg_namespace bn ON bn.oid = m.typnamespace
		WHERE n.nspname = ANY($1) AND t.typtype = 'c'
		ORDER BY n.nspname, t.typname, a.attnum`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: composites: %w", err)
	}
	defer rows.Close()
	byKey := map[string]*Composite{}
	for rows.Next() {
		var schema, name, formatted string
		var builtin, spatial bool
		f := &Column{}
		if err := rows.Scan(&schema, &name, &f.Name, &f.Position,
			&f.TypeSchema, &f.PGType, &f.BaseTypeSchema, &f.IsArray, &f.Comment,
			&f.Domain, &formatted, &builtin, &spatial); err != nil {
			return err
		}
		applyTypmod(f, formatted, builtin, spatial)
		key := schema + "." + name
		comp := byKey[key]
		if comp == nil {
			comp = &Composite{Schema: schema, Name: name}
			byKey[key] = comp
			cat.Composites = append(cat.Composites, comp)
		}
		comp.Fields = append(comp.Fields, f)
	}
	return rows.Err()
}

// readFunctions reads callable functions with all their parameters
// (proallargtypes, which is null when every parameter is IN), splitting them
// into input Args and output ReturnColumns. pronargdefaults counts the
// trailing input parameters that have defaults.
func readFunctions(ctx context.Context, db Querier, schemas []string, cat *Catalog) error {
	rows, err := db.Query(ctx, `
		SELECT n.nspname, p.proname, p.provolatile, p.proretset, rt.typname, rtn.nspname,
		       `+domainRef("rt")+`,
		       p.pronargdefaults,
		       ARRAY(SELECT COALESCE(p.proargnames[a.ord::int], '') FROM `+funcArgs+` ORDER BY a.ord),
		       ARRAY(SELECT COALESCE(p.proargmodes[a.ord::int], 'i')::text FROM `+funcArgs+` ORDER BY a.ord),
		       ARRAY(SELECT t.typname FROM `+funcArgs+` JOIN pg_type t ON t.oid = a.oid ORDER BY a.ord),
		       ARRAY(SELECT tn.nspname FROM `+funcArgs+`
		             JOIN pg_type t ON t.oid = a.oid
		             JOIN pg_namespace tn ON tn.oid = t.typnamespace ORDER BY a.ord),
		       ARRAY(SELECT COALESCE(pg_get_function_arg_default(p.oid, a.ord::int), '') FROM `+funcArgs+` ORDER BY a.ord),
		       ARRAY(SELECT `+domainRef("t")+` FROM `+funcArgs+` JOIN pg_type t ON t.oid = a.oid ORDER BY a.ord),
		       COALESCE(obj_description(p.oid, 'pg_proc'), '')
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_type rt ON rt.oid = p.prorettype
		JOIN pg_namespace rtn ON rtn.oid = rt.typnamespace
		WHERE n.nspname = ANY($1)
		  AND p.prokind = 'f'
		  AND p.pronargs = COALESCE(array_length(p.proargtypes, 1), 0)
		  AND rt.typname NOT IN ('trigger', 'event_trigger', 'internal')
		  AND has_function_privilege(p.oid, 'EXECUTE')
		  -- extension members (pgcrypto, uuid-ossp, ...) are implementation
		  -- detail, not API surface
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.objid = p.oid AND d.classid = 'pg_proc'::regclass
		                    AND d.deptype = 'e')
		  -- constructors CREATE TYPE ... AS RANGE generates (int4range(),
		  -- int4multirange(), ...) are owned by their type
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.objid = p.oid AND d.classid = 'pg_proc'::regclass
		                    AND d.refclassid = 'pg_type'::regclass AND d.deptype = 'i')
		ORDER BY n.nspname, p.proname`, schemas)
	if err != nil {
		return fmt.Errorf("introspect: functions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		f := &Function{}
		var vol rune
		var nDefaults int
		var names, modes, types, typeSchemas, defaults, domains []string
		if err := rows.Scan(&f.Schema, &f.Name, &vol, &f.ReturnsSet, &f.ReturnType, &f.ReturnTypeSchema, &f.ReturnDomain,
			&nDefaults, &names, &modes, &types, &typeSchemas, &defaults, &domains, &f.Comment); err != nil {
			return err
		}
		switch vol {
		case 'i':
			f.Volatility = VolatilityImmutable
		case 's':
			f.Volatility = VolatilityStable
		default:
			f.Volatility = VolatilityVolatile
		}
		if buildFuncArgs(f, nDefaults, names, modes, types, typeSchemas, defaults, domains) {
			cat.Functions = append(cat.Functions, f)
		}
	}
	return rows.Err()
}

// domainRef selects the "schema.name" of the domain the pg_type alias typ
// is, or of its element domain when typ is an array of a domain, and the
// empty string for any other type. A domain over an array is itself a
// domain (typtype 'd').
func domainRef(typ string) string {
	return fmt.Sprintf(`COALESCE((SELECT dn.nspname || '.' || dt.typname
		         FROM pg_type dt JOIN pg_namespace dn ON dn.oid = dt.typnamespace
		         WHERE dt.typtype = 'd'
		           AND dt.oid = CASE WHEN %[1]s.typtype = 'd' THEN %[1]s.oid
		                             WHEN %[1]s.typcategory = 'A' THEN %[1]s.typelem END), '')`, typ)
}

// funcArgs is the FROM item over every parameter of pg_proc p, one row
// a(oid, ord) per parameter with ord its 1-based position.
const funcArgs = `unnest(COALESCE(p.proallargtypes, p.proargtypes::oid[])) WITH ORDINALITY AS a(oid, ord)`

// buildFuncArgs fills f.Args and f.ReturnColumns from parallel per-parameter
// arrays. It reports false for a function with an unnamed input parameter,
// which named notation and named API arguments cannot address.
func buildFuncArgs(f *Function, nDefaults int, names, modes, types, typeSchemas, defaults, domains []string) bool {
	outs := 0
	for i, typ := range types {
		a := FuncArg{Name: names[i], PGType: typ, TypeSchema: typeSchemas[i], Default: defaults[i], Domain: domains[i]}
		switch modes[i] {
		case "o":
			a.Mode = ArgModeOut
		case "b":
			a.Mode = ArgModeInOut
		case "v":
			a.Mode = ArgModeVariadic
		case "t":
			a.Mode = ArgModeTable
		default:
			a.Mode = ArgModeIn
		}
		if a.Mode != ArgModeIn && a.Mode != ArgModeVariadic {
			outs++
			out := a
			out.Default = ""
			if out.Name == "" {
				out.Name = fmt.Sprintf("column%d", outs)
			}
			f.ReturnColumns = append(f.ReturnColumns, out)
		}
		if a.Mode != ArgModeOut && a.Mode != ArgModeTable {
			if a.Name == "" {
				return false
			}
			f.Args = append(f.Args, a)
		}
	}
	for i := max(len(f.Args)-nDefaults, 0); i < len(f.Args); i++ {
		f.Args[i].HasDefault = true
	}
	return true
}
