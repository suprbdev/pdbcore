package pgexec

import "testing"

func TestServerMajor(t *testing.T) {
	for v, want := range map[string]int{
		"18.1":                           18,
		"19beta1":                        19,
		"16.4 (Debian 16.4-1.pgdg120+2)": 16,
		"13.16":                          13,
		"9.6.24":                         9,
		"":                               0,
		"devel":                          0,
	} {
		if got := serverMajor(v); got != want {
			t.Errorf("serverMajor(%q) = %d, want %d", v, got, want)
		}
	}
}
