package catalog_test

import (
	"reflect"
	"testing"

	"github.com/suprbdev/pdbcore/smartcomments/catalog"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbcore/testutil"
)

func TestCheck(t *testing.T) {
	c := testutil.FixtureCatalog()
	users := c.Table("public", "users")
	users.Comment = "@omit reed\n@behavior -filterBy -selectt"
	users.Column("email").Comment = "@behavior -order\nThe address."
	c.Table("public", "posts").ForeignKeys[0].Comment = "@omit many,fliter"
	c.Functions[0].Comment = "@behavior +nothing"
	vocab := smarttags.Vocabulary{OmitActions: []string{"read", "many", "filter", "order"}}

	var got []string
	for _, f := range catalog.Check(c, vocab) {
		got = append(got, f.Message()+" "+formatArgs(f.Args()))
	}
	fk := c.Table("public", "posts").ForeignKeys[0].Name
	fn := c.Functions[0]
	want := []string{
		"unknown @omit action ignored constraint=public.posts." + fk + " action=fliter",
		"unknown @omit action ignored table=public.users action=reed",
		"unknown @behavior token ignored table=public.users token=-selectt",
		"deprecated @behavior token column=public.users.email token=-order use=-orderBy",
		"unknown @behavior token ignored function=" + fn.Schema + "." + fn.Name + " token=+nothing",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Check =\n%q\nwant\n%q", got, want)
	}
}

func formatArgs(args []any) string {
	var s string
	for i := 0; i+1 < len(args); i += 2 {
		if s != "" {
			s += " "
		}
		s += args[i].(string) + "=" + args[i+1].(string)
	}
	return s
}
