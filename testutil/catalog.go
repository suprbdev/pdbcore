// Package testutil provides the shared test fixture: FixtureSQL (the DDL
// both products' compose stacks and e2e suites load) and FixtureCatalog, a
// hand-built Catalog mirroring what introspecting that DDL yields, so
// builder and compiler tests run without a database. The drift gate in
// drift_test.go keeps the two aligned.
package testutil

import (
	_ "embed"

	"github.com/suprbdev/pdbcore/introspect"
)

// FixtureSQL is the fixture schema DDL (embedded from fixture.sql).
//
//go:embed fixture.sql
var FixtureSQL string

// FixtureCatalog models FixtureSQL after introspection: users (pk id, unique
// email, enum mood + mood[], jsonb, text[], composite address + address[]),
// posts (pk id, fk author_id -> users.id, RLS), comments (self-referential),
// places (PostGIS geometry + geography, GiST-indexed), events (type_code
// bound to the table-backed event_type enum), the metrics view, the jwt
// composite, and the functions. Slices follow introspection order (indexes
// and constraints by name); PostGIS's own relations/composites are absent.
func FixtureCatalog() *introspect.Catalog {
	users := &introspect.Table{
		Schema: "public", Name: "users", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, Generated: true},
			{Name: "email", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "full_name", Position: 3, PGType: "text", TypeSchema: "pg_catalog"},
			{Name: "mood", Position: 4, PGType: "mood", TypeSchema: "public"},
			{Name: "settings", Position: 5, PGType: "jsonb", TypeSchema: "pg_catalog"},
			{Name: "tags", Position: 6, PGType: "_text", TypeSchema: "pg_catalog", IsArray: true},
			{Name: "balance", Position: 7, PGType: "int8", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true},
			{Name: "created_at", Position: 8, PGType: "timestamptz", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true},
			{Name: "address", Position: 9, PGType: "address", TypeSchema: "public"},
			{Name: "prev_addresses", Position: 10, PGType: "_address", TypeSchema: "public", IsArray: true},
			{Name: "moods", Position: 11, PGType: "_mood", TypeSchema: "public", IsArray: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "users_pkey", Columns: []string{"id"}},
		Uniques:    []*introspect.Constraint{{Name: "users_email_key", Columns: []string{"email"}}},
		Indexes: []*introspect.Index{
			{Name: "users_email_key", Columns: []string{"email"}, Unique: true, Method: "btree"},
			{Name: "users_mood_idx", Columns: []string{"mood"}, Method: "btree"},
			{Name: "users_moods_idx", Columns: []string{"moods"}, Method: "gin"},
			{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "users_settings_idx", Columns: []string{"settings"}, Method: "gin"},
			{Name: "users_tags_idx", Columns: []string{"tags"}, Method: "gin"},
		},
	}
	posts := &introspect.Table{
		Schema: "public", Name: "posts", Kind: introspect.RelTable,
		RLSEnabled: true,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, Generated: true},
			{Name: "author_id", Position: 2, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "title", Position: 3, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "body", Position: 4, PGType: "text", TypeSchema: "pg_catalog"},
			{Name: "published", Position: 5, PGType: "bool", TypeSchema: "pg_catalog", NotNull: true, HasDefault: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "posts_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "posts_author_id_fkey", Columns: []string{"author_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
		}},
		Indexes: []*introspect.Index{
			{Name: "posts_author_id_idx", Columns: []string{"author_id"}, Method: "btree"},
			{Name: "posts_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "posts_title_idx", Columns: []string{"title"}, Method: "btree"},
		},
	}
	// comments is self-referential (parent_id -> comments.id): a Reddit-style
	// reply tree. It exists so the cost estimator can be tested against
	// recursive nesting, where the default page^depth assumption explodes.
	comments := &introspect.Table{
		Schema: "public", Name: "comments", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, Generated: true},
			{Name: "post_id", Position: 2, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "parent_id", Position: 3, PGType: "int4", TypeSchema: "pg_catalog"},
			{Name: "body", Position: 4, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
		},
		PrimaryKey: &introspect.Constraint{Name: "comments_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "comments_parent_id_fkey", Columns: []string{"parent_id"},
			RefSchema: "public", RefTable: "comments", RefColumns: []string{"id"},
			Comment: "@costMultiplier 3",
		}, {
			Name: "comments_post_id_fkey", Columns: []string{"post_id"},
			RefSchema: "public", RefTable: "posts", RefColumns: []string{"id"},
		}},
		Indexes: []*introspect.Index{
			{Name: "comments_parent_id_idx", Columns: []string{"parent_id"}, Method: "btree"},
			{Name: "comments_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "comments_post_id_idx", Columns: []string{"post_id"}, Method: "btree"},
		},
	}
	// places carries the PostGIS columns: geometry (planar, SRID units) and
	// geography (spheroidal, metres), both GiST-indexed so the indexed-only
	// filter policy admits them.
	places := &introspect.Table{
		Schema: "public", Name: "places", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, Generated: true},
			{Name: "owner_id", Position: 2, PGType: "int4", TypeSchema: "pg_catalog"},
			{Name: "name", Position: 3, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "location", Position: 4, PGType: "geometry", TypeSchema: "public"},
			{Name: "area", Position: 5, PGType: "geography", TypeSchema: "public"},
		},
		PrimaryKey: &introspect.Constraint{Name: "places_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "places_owner_id_fkey", Columns: []string{"owner_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
		}},
		Indexes: []*introspect.Index{
			{Name: "places_area_idx", Columns: []string{"area"}, Method: "gist"},
			{Name: "places_location_idx", Columns: []string{"location"}, Method: "gist"},
			{Name: "places_owner_id_idx", Columns: []string{"owner_id"}, Method: "btree"},
			{Name: "places_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
		},
	}
	// events references the event_type @enum table. The fixture carries the
	// post-introspection state: the enum table itself is gone (converted to
	// the table-backed enum below), the FK is dropped, and type_code carries
	// the EnumRef binding instead.
	events := &introspect.Table{
		Schema: "public", Name: "events", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true, Generated: true},
			{Name: "name", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "type_code", Position: 3, PGType: "text", TypeSchema: "pg_catalog", NotNull: true, EnumRef: "public.event_type"},
		},
		PrimaryKey: &introspect.Constraint{Name: "events_pkey", Columns: []string{"id"}},
		Indexes: []*introspect.Index{
			{Name: "events_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "events_type_code_idx", Columns: []string{"type_code"}, Method: "btree"},
		},
	}
	// metrics is a PK-less view: no node identity, offset pagination only.
	// View columns carry no NOT NULL in pg_catalog, and the owner holds
	// every privilege bit even on a view (Insertable/Updatable/Deletable
	// gate on Kind, not on the bits).
	metrics := &introspect.Table{
		Schema: "public", Name: "metrics", Kind: introspect.RelView,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "name", Position: 1, PGType: "text", TypeSchema: "pg_catalog"},
			{Name: "value", Position: 2, PGType: "int4", TypeSchema: "pg_catalog"},
		},
	}
	return &introspect.Catalog{
		FormatVersion: introspect.CatalogFormatVersion,
		ServerVersion: "16.0",
		Schemas:       []string{"public"},
		Tables:        []*introspect.Table{comments, events, metrics, places, posts, users},
		Enums: []*introspect.Enum{{
			Schema: "public", Name: "event_type", TableBacked: true,
			Comment: "@enum\nKind of event.",
			Values:  []string{"conference", "hackathon", "meetup"},
			ValueDescriptions: []string{
				"A large formal gathering", "", "Casual get-together",
			},
		}, {
			Schema: "public", Name: "mood", Values: []string{"sad", "ok", "happy"},
		}},
		Composites: []*introspect.Composite{{
			Schema: "public", Name: "address",
			Fields: []*introspect.Column{
				{Name: "street", Position: 1, PGType: "text", TypeSchema: "pg_catalog"},
				{Name: "city", Position: 2, PGType: "text", TypeSchema: "pg_catalog"},
				{Name: "mood", Position: 3, PGType: "mood", TypeSchema: "public"},
			},
		}, {
			// JWT mint composite (rls.auth.jwt_type = public.jwt).
			Schema: "public", Name: "jwt",
			Fields: []*introspect.Column{
				{Name: "exp", Position: 1, PGType: "int8", TypeSchema: "pg_catalog"},
				{Name: "user_id", Position: 2, PGType: "int4", TypeSchema: "pg_catalog"},
				{Name: "role", Position: 3, PGType: "text", TypeSchema: "pg_catalog"},
			},
		}},
		Functions: []*introspect.Function{{
			Schema: "public", Name: "search_posts",
			Args:       []introspect.FuncArg{{Name: "term", PGType: "text", TypeSchema: "pg_catalog"}},
			ReturnType: "posts", ReturnTypeSchema: "public", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		}, {
			// Computed column: first arg is users' row type -> User.postCount.
			Schema: "public", Name: "users_post_count",
			Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public"}},
			ReturnType: "int8", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityStable,
		}, {
			// Set-returning computed column over a table -> User.recentPosts(n:).
			Schema: "public", Name: "users_recent_posts",
			Args: []introspect.FuncArg{
				{Name: "u", PGType: "users", TypeSchema: "public"},
				{Name: "n", PGType: "int4", TypeSchema: "pg_catalog"},
			},
			ReturnType: "posts", ReturnTypeSchema: "public", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		}, {
			// Set-returning computed column over a scalar -> User.tagWords.
			Schema: "public", Name: "users_tag_words",
			Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public"}},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		}, {
			// Volatile probe raising SQLSTATE 40001 on odd calls (retry e2e).
			Schema: "public", Name: "retry_probe",
			ReturnType: "int4", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Volatile scalar function -> Mutation.addNumbers(input:){result}.
			Schema: "public", Name: "add_numbers",
			Args: []introspect.FuncArg{
				{Name: "a", PGType: "int4", TypeSchema: "pg_catalog"},
				{Name: "b", PGType: "int4", TypeSchema: "pg_catalog"},
			},
			ReturnType: "int4", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Volatile table-returning function -> Mutation.publishPost(input:){result{...}}.
			Schema: "public", Name: "publish_post",
			Args:       []introspect.FuncArg{{Name: "post_id", PGType: "int4", TypeSchema: "pg_catalog"}},
			ReturnType: "posts", ReturnTypeSchema: "public",
			Volatility: introspect.VolatilityVolatile,
		}, {
			Schema: "public", Name: "unpublish_post",
			Args:       []introspect.FuncArg{{Name: "post_id", PGType: "int4", TypeSchema: "pg_catalog"}},
			ReturnType: "posts", ReturnTypeSchema: "public",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Array-typed argument -> list input.
			Schema: "public", Name: "word_lengths",
			Args:       []introspect.FuncArg{{Name: "words", PGType: "_text", TypeSchema: "pg_catalog"}},
			ReturnType: "int4", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Returns the jwt composite: minted into a token when
			// rls.auth.jwt_type = public.jwt.
			Schema: "public", Name: "authenticate",
			Args:       []introspect.FuncArg{{Name: "user_email", PGType: "text", TypeSchema: "pg_catalog"}},
			ReturnType: "jwt", ReturnTypeSchema: "public",
			Volatility: introspect.VolatilityVolatile,
		}, {
			// Computed column with an extra argument -> Post.excerpt(maxChars:).
			Schema: "public", Name: "posts_excerpt",
			Args: []introspect.FuncArg{
				{Name: "p", PGType: "posts", TypeSchema: "public"},
				{Name: "max_chars", PGType: "int4", TypeSchema: "pg_catalog"},
			},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityStable,
		}, {
			// Enum return -> Query.bestMood: Mood (not String).
			Schema: "public", Name: "best_mood",
			ReturnType: "mood", ReturnTypeSchema: "public",
			Volatility: introspect.VolatilityStable,
		}, {
			// Enum array return -> Query.allMoods: [Mood!].
			Schema: "public", Name: "all_moods",
			ReturnType: "_mood", ReturnTypeSchema: "public",
			Volatility: introspect.VolatilityStable,
		}, {
			// SETOF enum -> Query.moodMoods: [Mood!]!.
			Schema: "public", Name: "mood_moods",
			ReturnType: "mood", ReturnTypeSchema: "public", ReturnsSet: true,
			Volatility: introspect.VolatilityStable,
		}, {
			// Enum argument -> Query.describeMood(m: Mood).
			Schema: "public", Name: "describe_mood",
			Args:       []introspect.FuncArg{{Name: "m", PGType: "mood", TypeSchema: "public"}},
			ReturnType: "text", ReturnTypeSchema: "pg_catalog",
			Volatility: introspect.VolatilityStable,
		}, {
			// Volatile enum return -> Mutation.bumpMood(input:){result}.
			Schema: "public", Name: "bump_mood",
			Args:       []introspect.FuncArg{{Name: "m", PGType: "mood", TypeSchema: "public"}},
			ReturnType: "mood", ReturnTypeSchema: "public",
			Volatility: introspect.VolatilityVolatile,
		}},
	}
}
