// Package plugin defines the transport-neutral hook surfaces and the ordered
// registry. Products wrap Registry and add their own typed hooks (schema/API,
// compile, request); the catalog and inflection hooks live here because they
// only depend on introspect and inflect.
package plugin

import (
	"context"
	"sort"
	"sync"

	"github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
)

// Plugin is the base interface: identity plus default priority. Lower
// priority runs earlier (outermost in middleware chains); ties break by
// registration order.
type Plugin interface {
	Name() string
	Priority() int
}

// CatalogHook mutates or filters the introspected catalog before API
// building (e.g. hide tables, rename via comments).
type CatalogHook interface {
	TransformCatalog(ctx context.Context, c *introspect.Catalog) error
}

// InflectionHook controls every generated name. Call next to get the
// downstream (ultimately default) name; return your own to override.
type InflectionHook interface {
	Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string
}

// Registry holds plugins in deterministic order.
type Registry struct {
	mu      sync.Mutex
	plugins []Plugin
}

// NewRegistry builds a registry with the given plugins added in order.
func NewRegistry(plugins ...Plugin) *Registry {
	r := &Registry{}
	for _, p := range plugins {
		r.Add(p)
	}
	return r
}

// Add registers a plugin, keeping the list sorted by (priority, insertion).
func (r *Registry) Add(p Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins = append(r.plugins, p)
	sort.SliceStable(r.plugins, func(i, j int) bool {
		return r.plugins[i].Priority() < r.plugins[j].Priority()
	})
}

// All returns plugins in execution order.
func (r *Registry) All() []Plugin {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Plugin, len(r.plugins))
	copy(out, r.plugins)
	return out
}

// Enabled returns plugins in execution order, excluding disabled names.
func (r *Registry) Enabled(disabled map[string]bool) []Plugin {
	var out []Plugin
	for _, p := range r.All() {
		if !disabled[p.Name()] {
			out = append(out, p)
		}
	}
	return out
}

// TransformCatalog runs all CatalogHooks in order.
func (r *Registry) TransformCatalog(ctx context.Context, disabled map[string]bool, c *introspect.Catalog) error {
	for _, p := range r.Enabled(disabled) {
		if h, ok := p.(CatalogHook); ok {
			if err := h.TransformCatalog(ctx, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// Inflector composes all InflectionHooks into a single naming function with
// base (the product's default inflector) as the innermost link.
func (r *Registry) Inflector(disabled map[string]bool, base inflect.Next) inflect.Next {
	chain := base
	plugins := r.Enabled(disabled)
	// Build inside-out so lower priority ends up outermost.
	for i := len(plugins) - 1; i >= 0; i-- {
		h, ok := plugins[i].(InflectionHook)
		if !ok {
			continue
		}
		next := chain
		chain = func(kind inflect.Kind, in inflect.Input) string {
			return h.Inflect(kind, in, next)
		}
	}
	return chain
}
