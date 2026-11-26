package sql

import "bytes"

const (
	planFull   = "full table scan"
	planRange  = "primary key range scan"
	planLookup = "primary key lookup"
)

// keyRange est le planificateur de requêtes : il réduit la plage de clés à parcourir grâce aux conditions
// sur la clé primaire reliées par AND, comme id >= 10 AND id < 20. Le WHERE complet est quand même
// réévalué sur chaque ligne : la plage ne sert qu'à lire moins de pages, jamais à décider du résultat.
func keyRange(t *table, where Expr) (start, end []byte, plan string) {
	prefix := tablePrefix(t.name)
	start, end, plan = prefix, prefixEnd(prefix), planFull
	pk := t.cols[0]

	for _, c := range conjuncts(where) {
		op, lit, ok := pkComparison(c, pk.Name)
		if !ok || lit.Type != pk.Type {
			continue
		}
		k := append(append([]byte(nil), prefix...), encodeKey(lit)...)
		after := append(append([]byte(nil), k...), 0x00) // la première clé qui suit k
		switch op {
		case "=":
			start, end = maxKey(start, k), minKey(end, after)
		case ">":
			start = maxKey(start, after)
		case ">=":
			start = maxKey(start, k)
		case "<":
			end = minKey(end, k)
		case "<=":
			end = minKey(end, after)
		default:
			continue
		}
		if op == "=" {
			plan = planLookup
		} else if plan == planFull {
			plan = planRange
		}
	}
	return start, end, plan
}

func conjuncts(e Expr) []Expr {
	if e == nil {
		return nil
	}
	if b, ok := e.(Binary); ok && b.Op == "AND" {
		return append(conjuncts(b.Left), conjuncts(b.Right)...)
	}
	return []Expr{e}
}

// pkComparison reconnaît « pk op constante » et « constante op pk », en retournant l'opérateur dans le second cas.
func pkComparison(e Expr, pk string) (op string, lit Value, ok bool) {
	b, isBin := e.(Binary)
	if !isBin {
		return "", Value{}, false
	}
	if col, isCol := b.Left.(ColumnRef); isCol && col.Name == pk {
		if l, isLit := b.Right.(Literal); isLit {
			return b.Op, l.Value, true
		}
	}
	if col, isCol := b.Right.(ColumnRef); isCol && col.Name == pk {
		if l, isLit := b.Left.(Literal); isLit {
			flipped := map[string]string{"=": "=", "!=": "!=", "<": ">", "<=": ">=", ">": "<", ">=": "<="}
			return flipped[b.Op], l.Value, true
		}
	}
	return "", Value{}, false
}

func maxKey(a, b []byte) []byte {
	if bytes.Compare(a, b) >= 0 {
		return a
	}
	return b
}

func minKey(a, b []byte) []byte {
	if bytes.Compare(a, b) <= 0 {
		return a
	}
	return b
}
