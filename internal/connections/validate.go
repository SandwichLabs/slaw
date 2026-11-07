package connections

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/SandwichLabs/slaw/internal/session"
)

var (
	hostRE   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._:\-\[\]%]{0,252})$`)
	mdDBRE   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_\-]{0,127}$`)
	regionRE = regexp.MustCompile(`^[A-Za-z0-9\-_]{1,64}$`)
	endptRE  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._:\-]{0,252})$`)
	acctRE   = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
)

// noControl rejects NUL, newlines and other control characters in a value that
// will become a SQL literal or a path. The quoting helpers make injection
// impossible regardless; this keeps garbage out of config.
func noControl(field, v string) error {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}

func requireField(s session.Spec, field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s connection %q needs %s", s.Type, s.Name, field)
	}
	return noControl(field, v)
}

// secretName is the temporary secret that belongs to a connection. It is a
// function of the connection name only, so re-connecting replaces it.
func secretName(conn string) string {
	n := "slaw_" + conn
	if len(n) > 63 {
		sum := sha256.Sum256([]byte(conn))
		n = "slaw_" + hex.EncodeToString(sum[:])[:20]
	}
	return n
}

func parsePort(s session.Spec, def int) (int, error) {
	v := param(s, "port")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s connection %q: port %q is not a port number", s.Type, s.Name, v)
	}
	return n, nil
}

// unknownOptions rejects options a type does not understand: an option is a
// value that ends up in SQL, so the set is closed.
func unknownOptions(s session.Spec, allowed ...string) error {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range s.Params {
		if strings.HasPrefix(k, optPrefix) && !ok[strings.TrimPrefix(k, optPrefix)] {
			return fmt.Errorf("%s connection %q: unknown option %q (allowed: %s)", s.Type, s.Name, strings.TrimPrefix(k, optPrefix), strings.Join(allowed, ", "))
		}
	}
	return nil
}

// noStrayFields rejects the definition fields a type does not use. A field a
// type ignores is a field somebody else may read: a committed `scope: "file:///"`
// on a table source once widened what the agent could read (#119 review), so a
// type accepts exactly the top-level fields its Info declares.
func noStrayFields(s session.Spec, used ...string) error {
	ok := map[string]bool{}
	for _, u := range used {
		ok[u] = true
	}
	for _, f := range []string{"host", "port", "database", "region", "scope"} {
		if param(s, f) != "" && !ok[f] {
			return fmt.Errorf("%s connection %q: %q is not a setting of this type", s.Type, s.Name, f)
		}
	}
	if s.Path != "" && !ok["path"] {
		return fmt.Errorf("%s connection %q: \"path\" is not a setting of this type", s.Type, s.Name)
	}
	return nil
}

func oneOf(field, v string, choices ...string) error {
	if v == "" {
		return nil
	}
	for _, c := range choices {
		if v == c {
			return nil
		}
	}
	return fmt.Errorf("%s %q is not one of %s", field, v, strings.Join(choices, ", "))
}

// Validate checks a definition against its type with no network and no
// credential. The duckdb type is checked by the session registry.
func Validate(d Def) error {
	if err := session.ValidateName(d.Name); err != nil {
		return err
	}
	if d.Type == "duckdb" {
		if strings.TrimSpace(d.Path) == "" {
			return fmt.Errorf("duckdb connection %q needs a path", d.Name)
		}
		return noControl("path", d.Path)
	}
	t, ok := registry[d.Type]
	if !ok {
		return fmt.Errorf("unknown connection type %q (want one of %s)", d.Type, strings.Join(AllTypeNames(), ", "))
	}
	if err := distinctOptions(d); err != nil {
		return err
	}
	return t.Validate(d.Spec())
}

// distinctOptions refuses option names that are the same once lowercased:
// Def.Params lowercases them, so the value that won would depend on map order.
func distinctOptions(d Def) error {
	seen := map[string]string{}
	for k := range d.Options {
		lk := strings.ToLower(k)
		if other, dup := seen[lk]; dup {
			a, b := other, k
			if a > b {
				a, b = b, a
			}
			return fmt.Errorf("%s connection %q: options %q and %q are the same option written twice (names are not case-sensitive)", d.Type, d.Name, a, b)
		}
		seen[lk] = k
	}
	return nil
}
