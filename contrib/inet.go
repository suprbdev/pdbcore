package contrib

// Inet returns the vocabulary of an inet or cidr column. Operands are
// address or network strings ("10.0.0.0/8", "2001:db8::1"), compared as
// inet; a cidr column casts to inet implicitly.
func Inet(typeName string) (Vocabulary, bool) {
	if typeName != "inet" && typeName != "cidr" {
		return Vocabulary{}, false
	}
	idx := []IndexHint{{Method: "gist", OpClass: "inet_ops"}, {Method: "spgist", OpClass: "inet_ops"}}
	infix := func(name, op, desc string) Op {
		return Op{
			Name: name, Arg: ArgText, Index: idx, Description: desc,
			render: func(_ qual, col string, a []string) string { return col + " " + op + " " + a[0] + "::inet" },
		}
	}
	fn := func(name, cast string) func(qual, string) string {
		return func(_ qual, ref string) string { return name + "(" + ref + ")" + cast }
	}
	return Vocabulary{
		Name: VocabInet, Type: typeName,
		Ops: []Op{
			infix("containedBy", "<<", "Is a subnet of, or an address within, the given network, and not equal to it (<<)."),
			infix("containedByOrEquals", "<<=", "Is a subnet of, an address within, or equal to the given network (<<=)."),
			infix("contains", ">>", "Contains the given subnet or address, and is not equal to it (>>)."),
			infix("containsOrEquals", ">>=", "Contains or equals the given subnet or address (>>=)."),
			infix("containsOrContainedBy", "&&", "Contains or is contained by the given network (&&)."),
		},
		Accessors: []Accessor{
			{Name: "family", Type: "int4", Description: "Address family: 4 or 6 (family).", sql: fn("family", "")},
			{Name: "masklen", Type: "int4", Description: "Netmask length in bits (masklen).", sql: fn("masklen", "")},
			{Name: "host", Type: "text", Description: "The address without the netmask (host).", sql: fn("host", "")},
			{Name: "network", Type: "text", Description: "The network part of the address, as CIDR text (network).", sql: fn("network", "::text")},
		},
		Codec: &Codec{
			Arg: ArgText,
			sql: func(_ qual, a []string) string { return a[0] + "::" + typeName },
		},
	}, true
}
