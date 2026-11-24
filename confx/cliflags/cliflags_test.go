package cliflags_test

import (
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/confx/cliflags"
)

func TestChanged(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("config", "", "")
	fs.String("server.addr", ":8080", "")
	fs.Bool("server.compression", false, "")
	fs.StringSlice("schema.schemas", nil, "")
	fs.Duration("database.statement_timeout", time.Second, "")
	fs.Int("limits.max_depth", 10, "")
	if err := fs.Parse([]string{"--config=x.yaml", "--server.compression", "--schema.schemas=app,audit",
		"--database.statement_timeout=3s"}); err != nil {
		t.Fatal(err)
	}
	got := cliflags.Changed(fs, "config")
	want := map[string]any{
		"server.compression":         "true",
		"schema.schemas":             []string{"app", "audit"},
		"database.statement_timeout": "3s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Changed = %#v, want %#v", got, want)
	}

	var cfg struct {
		Server struct {
			Addr        string `koanf:"addr"`
			Compression bool   `koanf:"compression"`
		} `koanf:"server"`
		Schema struct {
			Schemas []string `koanf:"schemas"`
		} `koanf:"schema"`
		Database struct {
			StatementTimeout time.Duration `koanf:"statement_timeout"`
		} `koanf:"database"`
		Limits struct {
			MaxDepth int `koanf:"max_depth"`
		} `koanf:"limits"`
	}
	cfg.Server.Addr = ":9000"
	cfg.Limits.MaxDepth = 7
	if err := confx.Load("CLIFLAGS_TEST_", "", &cfg, confx.WithFlags(got)); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":9000" || !cfg.Server.Compression || !reflect.DeepEqual(cfg.Schema.Schemas, []string{"app", "audit"}) ||
		cfg.Database.StatementTimeout != 3*time.Second || cfg.Limits.MaxDepth != 7 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestExplain(t *testing.T) {
	var cfg struct {
		Limits struct {
			MaxCost int `koanf:"max_cost"`
		} `koanf:"limits"`
		Roles struct {
			Switch bool `koanf:"switch"`
		} `koanf:"roles"`
		Plugins struct {
			Settings struct {
				AdvancedFilters struct {
					Enabled bool `koanf:"enabled"`
				} `koanf:"advanced_filters"`
			} `koanf:"settings"`
		} `koanf:"plugins"`
	}
	opts := []confx.LoadOption{
		confx.WithMoved(map[string]string{
			"server.max_cost":                   "limits.max_cost",
			"plugins.settings.advanced-filters": "plugins.settings.advanced_filters",
		}),
		confx.WithRemoved(map[string]string{"transactions.mutations": "mutations always run in a transaction"}),
	}
	parse := func(args ...string) error {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.String("config", "", "")
		fs.Int("limits.max_cost", 0, "")
		return fs.Parse(args)
	}
	for _, c := range []struct{ arg, want string }{
		{"--rls.enabled=false", "unknown flag: --rls.enabled: moved to --roles.switch"},
		{"--server.max_cost=5", "unknown flag: --server.max_cost: moved to --limits.max_cost"},
		{"--plugins.settings.advanced-filters.enabled", "unknown flag: --plugins.settings.advanced-filters.enabled: moved to --plugins.settings.advanced_filters.enabled"},
		{"--transactions.mutations", "unknown flag: --transactions.mutations: removed: mutations always run in a transaction"},
		{"--limits.max_cots=1", "unknown flag: --limits.max_cots: did you mean --limits.max_cost?"},
		{"--nonsense.zzzz", "unknown flag: --nonsense.zzzz"},
		{"--confg=x", "unknown flag: --confg"},
	} {
		got := cliflags.Explain(parse(c.arg), &cfg, opts...)
		if got == nil || got.Error() != c.want {
			t.Errorf("Explain(%s) = %v, want %q", c.arg, got, c.want)
		}
	}
	if err := cliflags.Explain(nil, &cfg); err != nil {
		t.Fatalf("nil: %v", err)
	}
	other := errors.New("invalid argument")
	if got := cliflags.Explain(other, &cfg); !errors.Is(got, other) {
		t.Fatalf("other error changed: %v", got)
	}
}
