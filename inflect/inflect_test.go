package inflect

import "testing"

func TestCamel(t *testing.T) {
	cases := []struct{ in, upper, lower string }{
		{"user_accounts", "UserAccounts", "userAccounts"},
		{"id", "Id", "id"},
		{"created_at", "CreatedAt", "createdAt"},
		{"a_b_c", "ABC", "aBC"},
	}
	for _, c := range cases {
		if got := UpperCamel(c.in); got != c.upper {
			t.Errorf("UpperCamel(%q) = %q, want %q", c.in, got, c.upper)
		}
		if got := LowerCamel(c.in); got != c.lower {
			t.Errorf("LowerCamel(%q) = %q, want %q", c.in, got, c.lower)
		}
	}
}

func TestSingularizePluralize(t *testing.T) {
	cases := []struct{ plural, singular string }{
		{"users", "user"},
		{"categories", "category"},
		{"boxes", "box"},
		{"posts", "post"},
		{"people", "person"},
		{"organisation_people", "organisation_person"},
		{"children", "child"},
	}
	for _, c := range cases {
		if got := Singularize(c.plural); got != c.singular {
			t.Errorf("Singularize(%q) = %q, want %q", c.plural, got, c.singular)
		}
	}
	plurals := []struct{ singular, plural string }{
		{"category", "categories"},
		{"box", "boxes"},
		{"person", "people"},
		{"organisation_person", "organisation_people"},
		{"people", "people"}, // already plural stays put
	}
	for _, c := range plurals {
		if got := Pluralize(c.singular); got != c.plural {
			t.Errorf("Pluralize(%q) = %q, want %q", c.singular, got, c.plural)
		}
	}
	if got := Singularize("person"); got != "person" {
		t.Errorf("Singularize(person) = %q, want person", got)
	}
}

func TestEnumValue(t *testing.T) {
	cases := map[string]string{
		"very happy": "VERY_HAPPY",
		"1st":        "_1ST",
		"camelCase":  "CAMEL_CASE",
		"":           "_",
		"ok":         "OK",
	}
	for in, want := range cases {
		if got := EnumValue(in); got != want {
			t.Errorf("EnumValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestByColumns(t *testing.T) {
	if got := ByColumns([]string{"id"}); got != "Id" {
		t.Errorf("ByColumns(id) = %q", got)
	}
	if got := ByColumns([]string{"tenant_id", "email"}); got != "TenantIdAndEmail" {
		t.Errorf("ByColumns(tenant_id,email) = %q", got)
	}
}
