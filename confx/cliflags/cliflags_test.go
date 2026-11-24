package cliflags_test

import (
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
