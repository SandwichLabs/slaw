package session

import (
	"sort"
	"strings"
)

// ReferencedConnections are the registered connections a statement resolves
// names against, for a record of the run (a receipt): the connection whose
// catalog unqualified names mean (catalog, or the session default when it is
// empty) and every connection named in qualifier position (pg.public.orders).
// It is a best reading of the text, not a plan: a name that is only a column
// alias is not a qualifier (the same rule ConnectReferenced applies), and a
// connection that attaches no catalog (object storage) is named by a URL, so it
// is not listed. Sorted by name. Only types, names and states: no DSN, path or
// message.
func ReferencedConnections(s Session, catalog, sqlText string) []ConnectionInfo {
	if s == nil {
		return nil
	}
	if catalog == "" {
		catalog = s.DefaultCatalog()
	}
	named := qualifiers(sqlText)
	var out []ConnectionInfo
	for _, c := range s.ConnectionInfos() {
		if c.Catalogless {
			continue
		}
		if c.Name == catalog || named[strings.ToLower(c.Name)] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
