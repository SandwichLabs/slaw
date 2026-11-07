package bundle

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/SandwichLabs/slaw/internal/notebooks"
	"github.com/SandwichLabs/slaw/internal/queries"
	"github.com/SandwichLabs/slaw/internal/session"
	"github.com/SandwichLabs/slaw/internal/skills"
	"github.com/SandwichLabs/slaw/internal/workspace"
)

// Skill limits, the same ones skills.SaveUserSkillAt applies to a skill the
// agent or the editor writes.
const (
	skillMaxDescriptionRunes = 300
	skillMaxInstructions     = 32 * 1024
)

// CheckNotebook is the one validation path for a notebook document, whether it
// arrives in a bundle or as a single .slnb file: size cap, then the notebooks
// package's own whitelisting Coerce. The returned warnings are Coerce's
// (truncated titles and cells).
func CheckNotebook(data []byte) (notebooks.Notebook, []string, error) {
	if len(data) > MaxEntryBytes {
		return notebooks.Notebook{}, nil, fmt.Errorf("%w: a notebook is %d bytes, over the %d byte limit", ErrTooLarge, len(data), MaxEntryBytes)
	}
	nb, warnings, err := notebooks.Coerce(data)
	if err != nil {
		return notebooks.Notebook{}, warnings, fmt.Errorf("not a valid notebook: %w", err)
	}
	return nb, warnings, nil
}

var (
	builtinOnce  sync.Once
	builtinNames map[string]bool
)

func isBuiltinSkillName(name string) bool {
	builtinOnce.Do(func() {
		builtinNames = map[string]bool{}
		for _, s := range skills.Builtins() {
			builtinNames[s.Name] = true
		}
	})
	return builtinNames[name]
}

// validateItem parses one entry by the rules of the store that will hold it
// and returns a display title and any non-fatal warnings. An error means the
// whole bundle is refused.
func validateItem(kind ItemKind, id string, data []byte) (string, []string, error) {
	if len(data) > MaxEntryBytes {
		return "", nil, fmt.Errorf("%w: %s is %d bytes, over the %d byte limit", ErrTooLarge, entryPath(kind, id), len(data), MaxEntryBytes)
	}
	switch kind {
	case KindNotebook:
		nb, warnings, err := CheckNotebook(data)
		if err != nil {
			return "", nil, corrupt("%s: %v", entryPath(kind, id), err)
		}
		return nb.Title, prefixAll(warnings, entryPath(kind, id)), nil
	case KindQuery:
		q, err := queries.Decode(id, data)
		if err != nil {
			return "", nil, corrupt("%s: %v", entryPath(kind, id), err)
		}
		if q.Error != "" {
			return "", nil, corrupt("%s: %s", entryPath(kind, id), q.Error)
		}
		if strings.TrimSpace(q.Title) == "" {
			return "", nil, corrupt("%s: the query has no title", entryPath(kind, id))
		}
		if q.Connection != "" {
			if err := session.ValidateName(q.Connection); err != nil {
				return "", nil, corrupt("%s: %v", entryPath(kind, id), err)
			}
		}
		return q.Title, nil, nil
	case KindSkill:
		return validateSkill(id, data)
	case KindWatch:
		title, err := validateWatch(id, data)
		if err != nil {
			return "", nil, corrupt("%s: %v", entryPath(kind, id), err)
		}
		return title, nil, nil
	case KindSemantics:
		title, err := validateSemantics(id, data)
		if err != nil {
			return "", nil, corrupt("%s: %v", entryPath(kind, id), err)
		}
		return title, nil, nil
	}
	return "", nil, corrupt("unknown entry kind %q", kind)
}

func validateSkill(name string, data []byte) (string, []string, error) {
	p := entryPath(KindSkill, name)
	st, err := skills.Decode(name, data, nil)
	if err != nil {
		return "", nil, corrupt("%s: %v", p, err)
	}
	if strings.HasPrefix(name, skills.TaskPrefix) {
		return "", nil, corrupt("%s: the %q prefix is reserved for built-in skills", p, skills.TaskPrefix)
	}
	if isBuiltinSkillName(name) {
		return "", nil, corrupt("%s: %q is a built-in skill name and cannot be shadowed", p, name)
	}
	if strings.TrimSpace(st.Description) == "" || strings.ContainsAny(st.Description, "\r\n") {
		return "", nil, corrupt("%s: the description must be one non-empty line", p)
	}
	if n := len([]rune(st.Description)); n > skillMaxDescriptionRunes {
		return "", nil, corrupt("%s: the description is %d characters, over %d", p, n, skillMaxDescriptionRunes)
	}
	if strings.TrimSpace(st.Instructions) == "" {
		return "", nil, corrupt("%s: the skill has no instructions", p)
	}
	if len(st.Instructions) > skillMaxInstructions {
		return "", nil, corrupt("%s: the instructions are %d bytes, over %d", p, len(st.Instructions), skillMaxInstructions)
	}
	var warnings []string
	// Decode already turned a "built-in" claim into a plain user skill; say so.
	if doc, ok := skills.ParseDocument(string(data)); ok && doc.Meta["source"] == skills.SourceBuiltin {
		warnings = append(warnings, p+": claimed to be a built-in skill; it is imported as an ordinary skill")
	} else if ok {
		// Decode silently normalises any other unknown source to a user skill.
		switch src := doc.Meta["source"]; src {
		case "", skills.SourceUser, skills.SourceImport:
		default:
			warnings = append(warnings, fmt.Sprintf("%s: claims an unknown source %q; it is imported as an ordinary skill", p, src))
		}
	}
	return st.Name, warnings, nil
}

func prefixAll(ws []string, p string) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, p+": "+w)
	}
	return out
}

// --- connection definitions ---

type connectionsDoc struct {
	Version     int                       `yaml:"version"`
	Connections []workspace.ConnectionDef `yaml:"connections"`
}

// SanitizeConnection makes a definition fit to leave this computer, or says
// why it cannot. It keeps only what a definition is made of (name, type, host,
// port, database, region, scope, read_only, options) and applies the strictest
// rules there are, the committed slaw.yaml's: no absolute or out-of-workspace
// path, no ${ENV} (which could be pointed at an attacker's host and expand a
// secret into it), nothing credential-shaped. The sign-in suggestion is
// dropped, and so is any option whose value is an absolute path. The returned
// notes say what was changed.
func SanitizeConnection(d workspace.ConnectionDef) (workspace.ConnectionDef, []string, error) {
	var notes []string
	if d.Credentials != "" {
		d.Credentials = ""
		notes = append(notes, fmt.Sprintf("connection %q: the sign-in method was not included", d.Name))
	}
	if len(d.Options) > 0 {
		kept := make(map[string]string, len(d.Options))
		for k, v := range d.Options {
			if looksAbsolute(v) {
				notes = append(notes, fmt.Sprintf("connection %q: option %q was left out because it names a path on this computer", d.Name, k))
				continue
			}
			kept[k] = v
		}
		d.Options = kept
		if len(kept) == 0 {
			d.Options = nil
		}
	}
	if err := d.ValidateType(); err != nil {
		return workspace.ConnectionDef{}, notes, err
	}
	if workspace.MetadataDrivenType(d.Type) {
		notes = append(notes, fmt.Sprintf("connection %q: %s", d.Name, workspace.MetadataWarning))
	}
	if err := d.ValidateIn(workspace.ScopeShared); err != nil {
		return workspace.ConnectionDef{}, notes, err
	}
	return d, notes, nil
}

func looksAbsolute(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "~") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') && ((p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z')
}

func encodeConnections(defs []workspace.ConnectionDef) ([]byte, error) {
	b, err := yaml.Marshal(connectionsDoc{Version: workspace.ConfigVersion, Connections: defs})
	if err != nil {
		return nil, fmt.Errorf("failed to encode connection definitions: %w", err)
	}
	return append([]byte("# Connection definitions only. No credentials and no sign-in choices.\n"), b...), nil
}

// parseConnections reads connections.yaml. Unknown keys refuse the bundle (a
// sign_in or nonce key has no business here); a definition that fails
// SanitizeConnection is dropped with a warning, because connections are
// optional content and one bad one should not cost the rest.
func parseConnections(data []byte) ([]workspace.ConnectionDef, map[string]bool, []string, error) {
	var doc connectionsDoc
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, nil, corrupt("%s: %v", ConnectionsName, err)
	}
	if doc.Version > workspace.ConfigVersion {
		return nil, nil, nil, corrupt("%s: written by a newer version of Slaw", ConnectionsName)
	}
	var (
		out      []workspace.ConnectionDef
		warnings []string
		seen     = map[string]bool{}
		asked    = map[string]bool{}
	)
	for _, d := range doc.Connections {
		clean, notes, err := SanitizeConnection(d)
		warnings = append(warnings, notes...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("connection %q was not imported: %v", d.Name, err))
			continue
		}
		if seen[clean.Name] {
			warnings = append(warnings, fmt.Sprintf("connection %q appears twice; the second was ignored", clean.Name))
			continue
		}
		seen[clean.Name] = true
		// A bundle never makes a connection writable: it arrives read-only
		// and the person turns writes on, here, if they want them. (MotherDuck
		// cannot be attached read-only; its rule is left as it is.)
		if clean.Type != "motherduck" {
			if wantsWrite(clean) {
				asked[clean.Name] = true
				warnings = append(warnings, fmt.Sprintf("connection %q asked to be writable; it is imported read-only", clean.Name))
			}
			clean.ReadOnly = workspace.Bool(true)
		}
		out = append(out, clean)
	}
	return out, asked, warnings, nil
}

// maxTextRunes caps text that comes from the bundle's maker and is shown in
// Slaw's own dialogs (workspace name, createdBy, manifest notes, titles).
const (
	maxTextRunes = 120
	maxNoteRunes = 300
	maxNotes     = 20
)

// cleanText makes maker-supplied text safe to show beside Slaw's own: control
// characters, newlines and bidirectional or invisible formatting characters
// are removed, and the length is capped.
func cleanText(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == '\u2028', r == '\u2029':
			continue
		}
		if n++; n > limit {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// cleanNotes cleans and bounds the manifest's own warnings.
func cleanNotes(in []string) []string {
	var out []string
	for _, w := range in {
		if w = cleanText(w, maxNoteRunes); w == "" {
			continue
		}
		if len(out) == maxNotes {
			out = append(out, "…and more")
			break
		}
		out = append(out, w)
	}
	return out
}

var (
	broadScopeRE = regexp.MustCompile(`^[a-z0-9]+://[^/]*/*$`)
	falseyRE     = regexp.MustCompile(`(?i)^(false|0|off|no)$`)
)

// ConnectionDetails lists what a definition sets beyond its name and type, as
// "label value" lines, so a preview shows everything that decides where
// the connection goes and how: host, database, path, region, storage scope and
// every option.
func ConnectionDetails(d workspace.ConnectionDef) []string {
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, cleanText(k+" "+v, maxTextRunes))
		}
	}
	add("host", d.Host)
	if d.Port != 0 {
		add("port", fmt.Sprint(d.Port))
	}
	add("database", d.Database)
	add("path", d.Path)
	add("storage scope", d.Scope)
	add("region", d.Region)
	keys := make([]string, 0, len(d.Options))
	for k := range d.Options {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		add("option "+k, d.Options[k])
	}
	return out
}

// ConnectionRisks lists reasons to look twice at a definition: settings that
// weaken transport security, send storage traffic somewhere other than the
// cloud provider, or reach a whole bucket.
func ConnectionRisks(d workspace.ConnectionDef) []string {
	var out []string
	if v, ok := d.Options["use_ssl"]; ok && falseyRE.MatchString(strings.TrimSpace(v)) {
		out = append(out, "Turns TLS off (use_ssl=false): traffic and credentials are not encrypted.")
	}
	switch strings.ToLower(d.Options["sslmode"]) {
	case "disable", "allow", "prefer":
		out = append(out, fmt.Sprintf("sslmode=%s does not require an encrypted connection.", d.Options["sslmode"]))
	}
	switch d.Type {
	case "s3", "gcs", "r2":
		if e := strings.ToLower(strings.TrimSpace(d.Options["endpoint"])); e != "" && d.Type == "s3" &&
			!strings.HasSuffix(e, ".amazonaws.com") && !strings.HasSuffix(e, ".amazonaws.com.cn") {
			out = append(out, fmt.Sprintf("Sends storage requests, with your credentials, to %s instead of Amazon S3.", cleanText(e, 80)))
		}
		if d.Scope == "" || broadScopeRE.MatchString(d.Scope) {
			out = append(out, "Its storage scope is a whole bucket or broader: once you sign in, the agent can read anything in it.")
		}
	case "azure":
		if d.Scope == "" || broadScopeRE.MatchString(d.Scope) {
			out = append(out, "Its storage scope is a whole container or broader: once you sign in, the agent can read anything in it.")
		}
	case "mysql":
		switch strings.ToLower(d.Options["ssl_mode"]) {
		case "disabled", "preferred":
			out = append(out, fmt.Sprintf("ssl_mode=%s does not require an encrypted connection.", d.Options["ssl_mode"]))
		}
	case "ducklake":
		if dp := d.Options["data_path"]; strings.Contains(dp, "://") && broadScopeRE.MatchString(dp) {
			out = append(out, "Its data path is a whole bucket or container: the lake's files are read and written there.")
		}
	case "motherduck":
		out = append(out, "MotherDuck connections can be written to once you sign in; use a read-scaling token for read-only access.")
	}
	return out
}

// wantsWrite reports a definition that asks to be writable.
func wantsWrite(d workspace.ConnectionDef) bool { return d.ReadOnly != nil && !*d.ReadOnly }

func sortStrings(s []string) { sort.Strings(s) }

// PortableReason says in a sentence why SanitizeConnection refused a definition.
// It is classified here, beside the validation that words the refusal, so no
// adapter reads error text.
func PortableReason(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "absolute"):
		return "It points at a file path on this computer."
	case strings.Contains(msg, "leaves the workspace"):
		return "It points outside the workspace folder."
	case strings.Contains(msg, "${"):
		return "It reads a value from an environment variable."
	}
	return "It can't be shared as it is."
}
