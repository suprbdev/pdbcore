package introspect

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func genCatalog(n int) *Catalog {
	c := &Catalog{}
	for i := 0; i < n; i++ {
		c.Tables = append(c.Tables, &Table{Schema: "public", Name: fmt.Sprintf("t%d", i)})
		c.Enums = append(c.Enums, &Enum{Schema: "public", Name: fmt.Sprintf("e%d", i), TableBacked: i%2 == 0})
		c.Composites = append(c.Composites, &Composite{Schema: "public", Name: fmt.Sprintf("c%d", i)})
		c.Ranges = append(c.Ranges, &RangeType{Schema: "public", Name: fmt.Sprintf("r%d", i), Multirange: fmt.Sprintf("m%d", i), MultirangeSchema: "public"})
		c.Extensions = append(c.Extensions, &Extension{Name: fmt.Sprintf("x%d", i)})
	}
	return c
}

func TestLookupsFollowMutation(t *testing.T) {
	c := genCatalog(5)
	if c.Table("public", "t3") != c.Tables[3] || c.Table("public", "nope") != nil {
		t.Fatal("initial lookups wrong")
	}
	if c.Enum("public", "e2") == nil || c.EnumByRef("public.e2") == nil || c.EnumByRef("public.e1") != nil || c.EnumByRef("") != nil {
		t.Fatal("enum lookups wrong")
	}
	if c.Composite("public", "c1") == nil || c.Range("public", "r4") == nil || c.RangeOfMultirange("public", "m4") == nil || !c.HasExtension("x0") {
		t.Fatal("other lookups wrong")
	}

	added := &Table{Schema: "app", Name: "new"}
	c.Tables = append(c.Tables, added)
	if c.Table("app", "new") != added {
		t.Fatal("appended table not found")
	}
	removed := c.Tables[1]
	c.Tables = append(c.Tables[:1], c.Tables[2:]...)
	if c.Table(removed.Schema, removed.Name) != nil {
		t.Fatal("removed table still found")
	}
	c.Tables[0] = &Table{Schema: "public", Name: "swapped"}
	if c.Table("public", "swapped") == nil || c.Table("public", "t0") != nil {
		t.Fatal("replaced first element not tracked")
	}

	c.Tables[2].Name = "renamed"
	if c.Table("public", "t3") != nil && c.Table("public", "t3").Name != "t3" {
		t.Fatal("stale hit returned")
	}
	c.Reindex()
	if c.Table("public", "renamed") != c.Tables[2] {
		t.Fatal("rename not visible after Reindex")
	}

	dup := genCatalog(0)
	first, second := &Table{Schema: "s", Name: "d"}, &Table{Schema: "s", Name: "d"}
	dup.Tables = []*Table{first, second}
	if dup.Table("s", "d") != first {
		t.Fatal("duplicate keys must resolve to the first, as the linear scan did")
	}
}

func TestIndexNotSerialized(t *testing.T) {
	c := genCatalog(3)
	c.Table("public", "t1")
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "idx") {
		t.Fatalf("index leaked into JSON: %s", b)
	}
	var back Catalog
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Table("public", "t1") == nil {
		t.Fatal("unmarshalled catalog lookup failed")
	}
}

func TestLookupsConcurrent(t *testing.T) {
	c := genCatalog(200)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if c.Table("public", fmt.Sprintf("t%d", i)) == nil || c.Enum("public", fmt.Sprintf("e%d", i)) == nil ||
					c.Composite("public", fmt.Sprintf("c%d", i)) == nil || c.Range("public", fmt.Sprintf("r%d", i)) == nil {
					t.Error("lookup miss")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func benchLookup(b *testing.B, n int) {
	c := genCatalog(n)
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("t%d", i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if c.Table("public", names[i%n]) == nil {
			b.Fatal("miss")
		}
	}
}

func BenchmarkCatalogTable1000(b *testing.B) { benchLookup(b, 1000) }
func BenchmarkCatalogTable3000(b *testing.B) { benchLookup(b, 3000) }
