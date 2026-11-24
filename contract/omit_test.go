package contract

import (
	"testing"

	"github.com/suprbdev/pdbcore/smarttags"
)

func TestOmitAll(t *testing.T) {
	for _, comment := range []string{"", "A description.", "@name people\nPeople.", "@omit read"} {
		tags, desc := smarttags.Parse(omitAll(comment))
		if _, all := tags.Omits(); !all {
			t.Errorf("omitAll(%q) does not omit everything", comment)
		}
		_, wantDesc := smarttags.Parse(comment)
		if desc != wantDesc {
			t.Errorf("omitAll(%q) description = %q, want %q", comment, desc, wantDesc)
		}
	}
}
