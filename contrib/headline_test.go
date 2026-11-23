package contrib_test

import (
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/contrib"
)

func TestHeadlineSQL(t *testing.T) {
	tests := []struct {
		v       contrib.Vocabulary
		op      string
		doc     string
		options string
		args    []string
		want    string
	}{
		{contrib.FullText(), "matches", "t.body", "", []string{"$1"}, "ts_headline(t.body, to_tsquery($1::text))"},
		{contrib.FullText(), "matchesWebsearch", "t.body", "$3", []string{"$1", "$2"},
			"ts_headline($2::regconfig, t.body, websearch_to_tsquery($2::regconfig, $1::text), $3::text)"},
		{contrib.TextSearch("text"), "matchesPlain", "t.c", "$2", []string{"$1"}, "ts_headline(t.c, plainto_tsquery($1::text), $2::text)"},
		{contrib.JSONSearch("jsonb"), "matchesPhrase", "t.doc", "", []string{"$1", "$2"},
			"ts_headline($2::regconfig, t.doc, phraseto_tsquery($2::regconfig, $1::text))"},
	}
	for _, tc := range tests {
		op, ok := tc.v.Op(tc.op)
		if !ok {
			t.Fatalf("%s: no op %s", tc.v.Name, tc.op)
		}
		if got := op.HeadlineSQL(tc.doc, tc.options, tc.args...); got != tc.want {
			t.Errorf("%s %s: got %s, want %s", tc.v.Name, tc.op, got, tc.want)
		}
	}
	op, _ := contrib.FullText().Op("matches")
	if op.HeadlineSQL("t.body", "", "$1", "$2", "$3") != "" {
		t.Error("HeadlineSQL with too many placeholders")
	}
	op, _ = contrib.Hstore().Op("hasKey")
	if op.HeadlineSQL("t.c", "", "$1") != "" {
		t.Error("HeadlineSQL on a non-full-text op")
	}
}

func TestParseHeadlineOptions(t *testing.T) {
	tests := []struct {
		in   any
		want string
		err  string
	}{
		{nil, "", ""},
		{map[string]any{}, "", ""},
		{map[string]any{"maxWords": int64(20), "minWords": 5.0, "startSel": "<mark>", "stopSel": "</mark>"},
			`MaxWords=20, MinWords=5, StartSel="<mark>", StopSel="</mark>"`, ""},
		{map[string]any{"maxFragments": "3", "highlightAll": "false", "fragmentDelimiter": " … ", "shortWord": 0},
			`ShortWord=0, MaxFragments=3, HighlightAll=false, FragmentDelimiter=" … "`, ""},
		{map[string]any{"highlightAll": true, "maxWords": nil}, "HighlightAll=true", ""},
		{map[string]any{"maxWords": 10}, "", "minWords (15) must be less than maxWords (10)"},
		{map[string]any{"maxWords": 10, "minWords": 10}, "", "minWords (10) must be less than maxWords (10)"},
		{map[string]any{"maxWords": 101}, "", "between 1 and 100"},
		{map[string]any{"maxWords": 0}, "", "between 1 and 100"},
		{map[string]any{"minWords": 2.5}, "", "between 1 and 100"},
		{map[string]any{"maxFragments": 11}, "", "between 0 and 10"},
		{map[string]any{"shortWord": -1}, "", "between 0 and 100"},
		{map[string]any{"maxWords": int64(1) << 40}, "", "between 1 and 100"},
		{map[string]any{"highlightAll": "yes"}, "", "must be a boolean"},
		{map[string]any{"startSel": `<b class="x">`}, "", "without double quotes"},
		{map[string]any{"startSel": `\b`}, "", "without double quotes"},
		{map[string]any{"stopSel": "a\nb"}, "", "control characters"},
		{map[string]any{"stopSel": ""}, "", "of 1 to 32 bytes"},
		{map[string]any{"stopSel": strings.Repeat("x", 33)}, "", "of 1 to 32 bytes"},
		{map[string]any{"stopSel": 1}, "", "must be a string"},
		{map[string]any{"MaxWords": 5}, "", `unknown headline option "MaxWords"`},
		{"MaxWords=5", "", "must be an object"},
	}
	for _, tc := range tests {
		h, err := contrib.ParseHeadlineOptions(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%v: err %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", tc.in, err)
			continue
		}
		if got := h.String(); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsDocument(t *testing.T) {
	for typ, want := range map[string]bool{
		"text": true, "varchar": true, "bpchar": true, "citext": true, "json": true, "jsonb": true,
		"pg_catalog.jsonb": true, "_jsonb": false, "_text": false, "tsvector": false, "int4": false, "jsonpath": false,
	} {
		if got := contrib.IsDocument(typ); got != want {
			t.Errorf("IsDocument(%q) = %v, want %v", typ, got, want)
		}
	}
}
