package confx

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// ExampleOption customises ExampleYAML.
type ExampleOption func(*exampleOpts)

type exampleOpts struct {
	docs map[string]string
}

// WithDoc overrides the doc comment for one koanf path (e.g.
// "errors.detail") — for product-specific wording on shared sections.
func WithDoc(path, doc string) ExampleOption {
	return func(o *exampleOpts) {
		if o.docs == nil {
			o.docs = map[string]string{}
		}
		o.docs[path] = doc
	}
}

// ExampleYAML renders the commented reference configuration body from v's
// struct tags and current values: every `koanf` field in declaration order,
// its `doc` tag as a comment, nested structs as sections (blank line after
// each top-level one), embedded structs tagged `koanf:",squash"` (or
// untagged anonymous structs) inlined. Callers prepend their own header.
func ExampleYAML(v any, opts ...ExampleOption) string {
	var o exampleOpts
	for _, opt := range opts {
		opt(&o)
	}
	var b strings.Builder
	writeStruct(&b, reflect.Indirect(reflect.ValueOf(v)), 0, "", &o)
	return b.String()
}

func writeStruct(b *strings.Builder, v reflect.Value, indent int, prefix string, o *exampleOpts) {
	t := v.Type()
	pad := strings.Repeat("  ", indent)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("koanf")
		if f.Anonymous && f.Type.Kind() == reflect.Struct && (key == "" || strings.HasPrefix(key, ",")) {
			writeStruct(b, v.Field(i), indent, prefix, o)
			continue
		}
		if key == "" || key == "-" {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		doc := f.Tag.Get("doc")
		if d, ok := o.docs[path]; ok {
			doc = d
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Duration(0)) {
			if doc != "" {
				fmt.Fprintf(b, "%s# %s\n", pad, doc)
			}
			fmt.Fprintf(b, "%s%s:\n", pad, key)
			writeStruct(b, fv, indent+1, path, o)
			if indent == 0 {
				b.WriteString("\n")
			}
			continue
		}
		if doc != "" {
			fmt.Fprintf(b, "%s# %s\n", pad, doc)
		}
		fmt.Fprintf(b, "%s%s: %s\n", pad, key, yamlValue(fv))
	}
}

func yamlValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return fmt.Sprintf("%q", v.String())
	case reflect.Bool:
		return fmt.Sprintf("%v", v.Bool())
	case reflect.Int, reflect.Int32, reflect.Int64:
		if v.Type() == reflect.TypeOf(time.Duration(0)) {
			return fmt.Sprintf("%q", time.Duration(v.Int()).String())
		}
		return fmt.Sprintf("%d", v.Int())
	case reflect.Slice:
		if v.Len() == 0 {
			return "[]"
		}
		parts := make([]string, v.Len())
		for i := 0; i < v.Len(); i++ {
			parts[i] = yamlValue(v.Index(i))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case reflect.Map:
		if v.Len() == 0 {
			return "{}"
		}
		keys := make([]string, 0, v.Len())
		for _, k := range v.MapKeys() {
			keys = append(keys, fmt.Sprintf("%v", k.Interface()))
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s: %s", k, yamlValue(v.MapIndex(reflect.ValueOf(k))))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}
