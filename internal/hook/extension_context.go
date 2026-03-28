package hook

import (
	"sort"
	"strings"
)

// BuildExtensionContext collects session_context values from all
// extension namespaces and returns them joined. Returns empty
// string if no extensions provide session context.
func BuildExtensionContext(ext map[string]map[string]string) string {
	if len(ext) == 0 {
		return ""
	}

	namespaces := make([]string, 0, len(ext))
	for ns := range ext {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	var parts []string
	for _, ns := range namespaces {
		v, ok := ext[ns]["session_context"]
		if !ok {
			continue
		}
		v = strings.TrimRight(v, "\n")
		if v != "" {
			parts = append(parts, v)
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}
