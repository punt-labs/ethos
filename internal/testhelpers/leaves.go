package testhelpers

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Leaves are the leaves and keys of a decoded JSON document, counted per key
// path. A path is the first two key names above a value, each led by a dot:
// "" for the keys of the top map, ".a" for the keys of a's map, ".a.b" for
// everything at or below b ("a" and "b" being keys), so a leaf
// is matched against the leaves of the same field and not against the whole
// document. Leaves are written "s:text", "n:number", "b:true", "z:null", and a
// key as "k:name"; arrays share their parent's path.
type Leaves map[string]map[string]int

// CollectLeaves counts the leaves and keys of v, a value decoded with
// json.Decoder.UseNumber.
func CollectLeaves(v any) Leaves {
	out := Leaves{}
	collect(v, "", 0, out)
	return out
}

func collect(v any, path string, depth int, out Leaves) {
	add := func(leaf string) {
		if out[path] == nil {
			out[path] = map[string]int{}
		}
		out[path][leaf]++
	}
	switch x := v.(type) {
	case string:
		add("s:" + x)
	case json.Number:
		add("n:" + x.String())
	case bool:
		add(fmt.Sprintf("b:%v", x))
	case nil:
		add("z:null")
	case []any:
		for _, e := range x {
			collect(e, path, depth, out)
		}
	case map[string]any:
		for k, e := range x {
			add("k:" + k)
			child := path
			if depth < 2 {
				child = path + "." + k
			}
			collect(e, child, depth+1, out)
		}
	}
}

// LostLeaf reports a leaf that got has lost, or "" and false.
//
// Within each path present on both sides, leaves in both cancel and what is
// left of want must be matched one for one by what is left of got, so a value
// a pass rewrites counts as kept when its replacement stands in its place and
// a value that is dropped does not. The replacement is not predicted. A path
// present on one side only (a key renamed or removed) is pooled with the other
// such paths and matched the same way. A surplus in one field never covers a
// loss in another: a guard that rewrites x and drops y is a loss.
func LostLeaf(want, got Leaves) (string, bool) {
	lost := func(w, g map[string]int) (first string, missing, surplus int) {
		for leaf, n := range w {
			if d := n - g[leaf]; d > 0 {
				missing += d
				if first == "" || leaf < first {
					first = leaf
				}
			}
		}
		for leaf, n := range g {
			if d := n - w[leaf]; d > 0 {
				surplus += d
			}
		}
		return first, missing, surplus
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	orphanW, orphanG := map[string]int{}, map[string]int{}
	for _, p := range paths {
		g, both := got[p]
		if !both {
			for l, n := range want[p] {
				orphanW[l] += n
			}
			continue
		}
		if first, missing, surplus := lost(want[p], g); missing > surplus {
			return first, true
		}
	}
	for p, g := range got {
		if _, both := want[p]; !both {
			for l, n := range g {
				orphanG[l] += n
			}
		}
	}
	if first, missing, surplus := lost(orphanW, orphanG); missing > surplus {
		return first, true
	}
	return "", false
}
