package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
)

type named struct {
	name string
	prio int
}

func (n named) Name() string  { return n.name }
func (n named) Priority() int { return n.prio }

type catHook struct {
	named
	err error
}

func (h catHook) TransformCatalog(_ context.Context, c *introspect.Catalog) error {
	c.Schemas = append(c.Schemas, h.name)
	return h.err
}

type inflHook struct {
	named
	tag string
}

func (h inflHook) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
	return h.tag + "(" + next(kind, in) + ")"
}

func TestRegistryOrder(t *testing.T) {
	r := NewRegistry(named{"b", 100}, named{"a", 50}, named{"c", 100}, named{"d", 10})
	r.Add(named{"e", 50})
	var got []string
	for _, p := range r.All() {
		got = append(got, p.Name())
	}
	if strings.Join(got, ",") != "d,a,e,b,c" {
		t.Fatalf("order = %v, want priority then insertion", got)
	}
	var enabled []string
	for _, p := range r.Enabled(map[string]bool{"a": true, "c": true}) {
		enabled = append(enabled, p.Name())
	}
	if strings.Join(enabled, ",") != "d,e,b" {
		t.Fatalf("enabled = %v", enabled)
	}
}

func TestTransformCatalogRunsInOrderAndStopsOnError(t *testing.T) {
	boom := errors.New("boom")
	r := NewRegistry(catHook{named: named{"second", 20}}, catHook{named: named{"first", 10}},
		catHook{named: named{"third", 30}, err: boom}, catHook{named: named{"never", 40}})
	cat := &introspect.Catalog{}
	err := r.TransformCatalog(context.Background(), nil, cat)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(cat.Schemas, ",") != "first,second,third" {
		t.Fatalf("hooks ran %v", cat.Schemas)
	}
	cat = &introspect.Catalog{}
	if err := r.TransformCatalog(context.Background(), map[string]bool{"third": true}, cat); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cat.Schemas, ",") != "first,second,never" {
		t.Fatalf("disabled hook still ran or order wrong: %v", cat.Schemas)
	}
}

func TestInflectorChainsLowestPriorityOutermost(t *testing.T) {
	base := func(kind inflect.Kind, in inflect.Input) string { return string(kind) + ":" + in.Table }
	r := NewRegistry(inflHook{named{"outer", 10}, "o"}, named{"plain", 15}, inflHook{named{"inner", 20}, "i"})
	got := r.Inflector(nil, base)("k", inflect.Input{Table: "users"})
	if got != "o(i(k:users))" {
		t.Fatalf("chain = %q", got)
	}
	got = r.Inflector(map[string]bool{"outer": true}, base)("k", inflect.Input{Table: "users"})
	if got != "i(k:users)" {
		t.Fatalf("chain with outer disabled = %q", got)
	}
	if got := NewRegistry().Inflector(nil, base)("k", inflect.Input{Table: "t"}); got != "k:t" {
		t.Fatalf("empty registry must return base: %q", got)
	}
}
