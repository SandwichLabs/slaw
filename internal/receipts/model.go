package receipts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SandwichLabs/slaw/internal/jsonenc"
)

// Surface is where a run came from.
type Surface string

// The surfaces. "render" is an A2UI component or a notebook SQL cell: Go cannot
// tell them apart until a later slice passes a source.
const (
	SurfaceQueryEditor     Surface = "query_editor"
	SurfaceConsole         Surface = "console"
	SurfaceCLIQuery        Surface = "cli_query"
	SurfaceRender          Surface = "render"
	SurfaceArtifact        Surface = "artifact"
	SurfaceMetric          Surface = "metric"
	SurfaceAssertion       Surface = "assertion"
	SurfaceWatch           Surface = "watch"
	SurfaceAgentRunQuery   Surface = "agent_run_query"
	SurfaceAgentSavedQuery Surface = "agent_saved_query"
	SurfaceAgentPreview    Surface = "agent_preview"
)

// Actor is who asked.
type Actor string

// The actors.
const (
	ActorPerson Actor = "person"
	ActorAgent  Actor = "agent"
)

// SourceKind is what the person wrote.
type SourceKind string

// The source kinds. The empty kind is a source nobody validated (a render).
const (
	SourceSQL           SourceKind = "sql"
	SourceSavedQuery    SourceKind = "saved_query"
	SourceMetric        SourceKind = "metric"
	SourceArtifactQuery SourceKind = "artifact_query"
	SourceCell          SourceKind = "cell"
)

// Verdict is what the SQL policy said: read for a policy-gated run, unchecked
// for the person's own editor, console and CLI.
type Verdict string

// The verdicts.
const (
	VerdictRead      Verdict = "read"
	VerdictUnchecked Verdict = "unchecked"
)

// ConnStatus is a connection's state when the statement ran. It is an enum so a
// status message, which can name a host, never lands in a receipt.
type ConnStatus string

// The connection statuses.
const (
	StatusConnected    ConnStatus = "connected"
	StatusNotConnected ConnStatus = "not_connected"
	StatusError        ConnStatus = "error"
)

// SnapshotLabel says whether a snapshot fact can be replayed later.
type SnapshotLabel string

// The snapshot labels.
const (
	LabelReplayable  SnapshotLabel = "replayable"
	LabelLiveOnly    SnapshotLabel = "live_only"
	LabelNotRecorded SnapshotLabel = "not_recorded"
)

// Limits on a receipt's parts, checked by Validate so a store never holds a
// record a caller could grow without bound.
const (
	MaxTextBytes    = 64 << 10 // sql and run_sql, as engine.RecordableMax
	MaxParamBytes   = 256
	MaxParams       = 256
	MaxNameBytes    = 256 // a param, column, connection or type name
	MaxConnections  = 256
	MaxColumns      = 2048
	MaxRefBytes     = 512
	MaxSessionBytes = 128
	MaxFactBytes    = 256 // a snapshot's fact and value
)

// Source is what the person wrote: the kind and a file-relative id
// (queries/x.sql, artifacts/a#by_month), never an absolute path.
type Source struct {
	Kind SourceKind `json:"kind,omitempty"`
	Ref  string     `json:"ref,omitempty"`
}

// ParamValue is one bound value. Value is empty when it was withheld (it failed
// Recordable) or when the receipt keeps no values (Policy.KeepParams off).
type ParamValue struct {
	Value     string `json:"value,omitempty"`
	Withheld  bool   `json:"withheld,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Snapshot is one fact about a connection's state, filled by the connection
// collectors (a later slice); the model only holds it.
type Snapshot struct {
	Fact  string        `json:"fact"`
	Value string        `json:"value"`
	Label SnapshotLabel `json:"label"`
}

// Connection is one catalog the statement resolved to.
type Connection struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	ReadOnly bool       `json:"read_only"`
	Status   ConnStatus `json:"status"`
	Snapshot *Snapshot  `json:"snapshot,omitempty"`
}

// Rows counts the rows as shown: after the caller dropped its probe row and
// any internal rows.
type Rows struct {
	Returned  int64 `json:"returned"`
	Total     int64 `json:"total"`
	Truncated bool  `json:"truncated"`
}

// Column is a result column's name and type; never a value.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Receipt is the record of one run. Build one, apply a Policy to its
// parameters, then Seal it, which validates it and derives its id.
type Receipt struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`
	Surface    Surface   `json:"surface"`
	Actor      Actor     `json:"actor"`
	// AgentSession is the MCP transport session or chat conversation; only for
	// the agent.
	AgentSession string `json:"agent_session,omitempty"`
	Source       Source `json:"source"`

	SQL            string `json:"sql,omitempty"`
	SQLWithheld    bool   `json:"sql_withheld,omitempty"`
	RunSQL         string `json:"run_sql,omitempty"`
	RunSQLWithheld bool   `json:"run_sql_withheld,omitempty"`
	SQLHash        string `json:"sql_hash,omitempty"`
	RunSQLHash     string `json:"run_sql_hash,omitempty"`

	Params         map[string]ParamValue `json:"params,omitempty"`
	ParamsHash     string                `json:"params_hash,omitempty"`
	ParamsWithheld bool                  `json:"params_withheld,omitempty"`

	Connections []Connection `json:"connections,omitempty"`
	Verdict     Verdict      `json:"verdict"`
	Rows        Rows         `json:"rows"`
	Columns     []Column     `json:"columns,omitempty"`
	ResultHMAC  string       `json:"result_hmac"`
}

// ErrInvalid is wrapped by every refusal of Validate and Decode.
var ErrInvalid = errors.New("invalid receipt")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Valid reports whether s is a known surface.
func (s Surface) Valid() bool {
	switch s {
	case SurfaceQueryEditor, SurfaceConsole, SurfaceCLIQuery, SurfaceRender, SurfaceArtifact, SurfaceMetric,
		SurfaceAssertion, SurfaceWatch, SurfaceAgentRunQuery, SurfaceAgentSavedQuery, SurfaceAgentPreview:
		return true
	}
	return false
}

// Valid reports whether a is a known actor.
func (a Actor) Valid() bool { return a == ActorPerson || a == ActorAgent }

// Valid reports whether k is a known source kind or empty.
func (k SourceKind) Valid() bool {
	switch k {
	case "", SourceSQL, SourceSavedQuery, SourceMetric, SourceArtifactQuery, SourceCell:
		return true
	}
	return false
}

// Valid reports whether v is a known verdict.
func (v Verdict) Valid() bool { return v == VerdictRead || v == VerdictUnchecked }

// Valid reports whether s is a known connection status.
func (s ConnStatus) Valid() bool {
	return s == StatusConnected || s == StatusNotConnected || s == StatusError
}

// Valid reports whether l is a known snapshot label.
func (l SnapshotLabel) Valid() bool {
	return l == LabelReplayable || l == LabelLiveOnly || l == LabelNotRecorded
}

// ValidRef reports whether ref is a file-relative id: not absolute, no "..",
// no backslash, no control character, bounded. The empty ref is valid.
func ValidRef(ref string) bool {
	if len(ref) > MaxRefBytes || strings.Contains(ref, "..") || strings.Contains(ref, `\`) {
		return false
	}
	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") || (len(ref) >= 2 && ref[1] == ':') {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// IsHash reports whether s is a lowercase hex SHA-256.
func IsHash(s string) bool { return isHex(s, 64) }

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// HashText is the SHA-256 of text, as lowercase hex: the sql_hash and
// run_sql_hash.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// HashParams is the params_hash: the SHA-256 over the full set of names and
// values (before any cap or withholding), length-prefixed and sorted by name so
// it is unambiguous and order-free.
func HashParams(params map[string]string) string {
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}
	sort.Strings(names)
	enc := appendUint(nil, uint64(len(names)))
	for _, n := range names {
		enc = appendString(enc, n)
		enc = appendString(enc, params[n])
	}
	sum := sha256.Sum256(enc)
	return hex.EncodeToString(sum[:])
}

// ValidID reports whether id has the form r_ + 20 lowercase hex digits.
func ValidID(id string) bool {
	return strings.HasPrefix(id, "r_") && isHex(id[2:], 20)
}

// ComputeID is the receipt's content address: r_ and the first 20 hex digits
// of the SHA-256 of the canonical encoding with id empty. It is not a
// signature.
func ComputeID(r Receipt) (string, error) {
	r.ID = ""
	b, err := jsonenc.Encode(r, "")
	if err != nil {
		return "", fmt.Errorf("failed to encode the receipt: %w", err)
	}
	sum := sha256.Sum256(b)
	return "r_" + hex.EncodeToString(sum[:])[:20], nil
}

// Seal validates r and sets its id (and puts At in UTC, so the encoding is
// stable). A receipt is sealed after its Policy is applied: changing a sealed
// receipt breaks its id.
func Seal(r Receipt) (Receipt, error) {
	r.At = r.At.UTC()
	r.ID = ""
	if err := r.validateBody(); err != nil {
		return Receipt{}, err
	}
	id, err := ComputeID(r)
	if err != nil {
		return Receipt{}, err
	}
	r.ID = id
	return r, nil
}

// Validate refuses a receipt that is not well formed: an unknown enum, a bad
// source ref, an over-long part, a malformed hash, or an id that is not its
// content address.
func (r Receipt) Validate() error {
	if !ValidID(r.ID) {
		return invalid("id %q is not a receipt id", r.ID)
	}
	if err := r.validateBody(); err != nil {
		return err
	}
	want, err := ComputeID(r)
	if err != nil {
		return err
	}
	if r.ID != want {
		return invalid("id does not match the record")
	}
	return nil
}

func (r Receipt) validateBody() error {
	switch {
	case !r.Surface.Valid():
		return invalid("unknown surface %q", r.Surface)
	case !r.Actor.Valid():
		return invalid("unknown actor %q", r.Actor)
	case !r.Verdict.Valid():
		return invalid("unknown verdict %q", r.Verdict)
	case r.DurationMS < 0:
		return invalid("negative duration")
	case len(r.AgentSession) > MaxSessionBytes:
		return invalid("agent session too long")
	case r.AgentSession != "" && r.Actor != ActorAgent:
		return invalid("only an agent has a session")
	case !IsHash(r.ResultHMAC):
		return invalid("result_hmac is not a hex digest")
	}
	for _, check := range []func() error{r.validateSource, r.validateText, r.validateParams, r.validateResult, r.validateConnections} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (r Receipt) validateSource() error {
	if !r.Source.Kind.Valid() {
		return invalid("unknown source kind %q", r.Source.Kind)
	}
	if !ValidRef(r.Source.Ref) {
		return invalid("source ref must be a relative id inside the workspace")
	}
	return nil
}

func validateText(name, text string, withheld bool, hash string) error {
	switch {
	case len(text) > MaxTextBytes:
		return invalid("%s too long", name)
	case withheld && text != "":
		return invalid("%s is withheld but present", name)
	case (text != "" || withheld) && !IsHash(hash):
		return invalid("%s has no hash", name)
	case hash != "" && !IsHash(hash):
		return invalid("%s hash is not a hex digest", name)
	}
	return nil
}

func (r Receipt) validateText() error {
	if err := validateText("sql", r.SQL, r.SQLWithheld, r.SQLHash); err != nil {
		return err
	}
	return validateText("run_sql", r.RunSQL, r.RunSQLWithheld, r.RunSQLHash)
}

func (r Receipt) validateParams() error {
	if len(r.Params) > MaxParams {
		return invalid("too many params")
	}
	if len(r.Params) > 0 && !IsHash(r.ParamsHash) {
		return invalid("params have no hash")
	}
	if r.ParamsHash != "" && !IsHash(r.ParamsHash) {
		return invalid("params_hash is not a hex digest")
	}
	for name, p := range r.Params {
		switch {
		case name == "" || len(name) > MaxNameBytes:
			return invalid("a param name is empty or too long")
		case len(p.Value) > MaxParamBytes:
			return invalid("param %q value too long", name)
		case p.Withheld && p.Value != "":
			return invalid("param %q is withheld but present", name)
		case r.ParamsWithheld && (p.Value != "" || p.Truncated):
			return invalid("params are withheld but param %q has a value", name)
		}
	}
	return nil
}

func (r Receipt) validateResult() error {
	if r.Rows.Returned < 0 || r.Rows.Total < r.Rows.Returned {
		return invalid("row counts are inconsistent")
	}
	if len(r.Columns) > MaxColumns {
		return invalid("too many columns")
	}
	for _, c := range r.Columns {
		if len(c.Name) > MaxNameBytes || len(c.Type) > MaxNameBytes {
			return invalid("a column name or type is too long")
		}
	}
	return nil
}

func (r Receipt) validateConnections() error {
	if len(r.Connections) > MaxConnections {
		return invalid("too many connections")
	}
	for _, c := range r.Connections {
		switch {
		case c.Name == "" || len(c.Name) > MaxNameBytes || len(c.Type) > MaxNameBytes:
			return invalid("a connection name or type is empty or too long")
		case !c.Status.Valid():
			return invalid("unknown connection status %q", c.Status)
		}
		if s := c.Snapshot; s != nil {
			switch {
			case !s.Label.Valid():
				return invalid("unknown snapshot label %q", s.Label)
			case len(s.Fact) > MaxFactBytes || len(s.Value) > MaxFactBytes:
				return invalid("a snapshot fact is too long")
			}
		}
	}
	return nil
}
