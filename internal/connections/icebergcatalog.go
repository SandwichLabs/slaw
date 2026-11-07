package connections

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SandwichLabs/slaw/internal/connname"
	"github.com/SandwichLabs/slaw/internal/secrets"
	"github.com/SandwichLabs/slaw/internal/session"
)

// icebergCatalogType attaches an Iceberg catalog: tables are listed and read
// through the catalog, not through a table folder (that is the iceberg type).
//
//	ATTACH '<arn>' AS "s3t" (TYPE iceberg, ENDPOINT_TYPE 's3_tables', SECRET 'slaw_<storage>',
//	    ACCESS_DELEGATION_MODE '<mode>', READ_ONLY)
//	ATTACH '<warehouse>' AS "x" (TYPE iceberg, ENDPOINT '<uri>', SECRET 'slaw_x' | AUTHORIZATION_TYPE 'none',
//	    ACCESS_DELEGATION_MODE '<mode>', READ_ONLY)
//
// Two kinds (option endpoint_type): s3_tables (Amazon S3 Tables, signed with the
// secret of the s3 connection named by option storage, so the credential lives
// once, on that connection) and rest (an Iceberg REST catalog with a bearer
// token, OAuth2 client credentials or no authentication). Glue is refused as
// not supported yet.
//
// A catalog's tables are written by whoever can commit to them, and their
// metadata names the files that are read (a local path, a URL, another bucket).
// So the type is read-only (Validate refuses read_only: false, the one place
// that covers save, open and a hand edit) and trust_writers says whether the
// agent may read the catalog on the main hub (true) or only on the confined lake
// reader (false, the default; internal/connections only records the choice, the
// routing is the agent's). Only the person can add one (workspace.OwnScopeOnlyType:
// a committed slaw.yaml and a bundle refuse it), and until the lake reader (L4)
// exists the agent's policy refuses every statement that names one.
//
// The ACCESS_DELEGATION_MODE is always written out, never left to DuckDB's
// default, and comes from where the catalog attaches, not from what the person
// wrote: see Delegation.
type icebergCatalogType struct{}

func (icebergCatalogType) Name() string { return "iceberg_catalog" }
func (icebergCatalogType) Remote() bool { return true }

// Extensions: httpfs, avro and iceberg are enough for REST and for S3 Tables
// signed with an explicit s3 secret; `aws` is only for credential_chain, which
// Slaw does not use (lakehouse plan, S0 item 1). avro is a named extension
// because the hub runs with autoinstall off.
func (icebergCatalogType) Extensions() []string { return []string{"httpfs", "avro", "iceberg"} }

// SpecExtensions implements session.SpecExtensionsType: the list does not depend
// on the spec, but the type is listed here so a future kind can widen it.
func (t icebergCatalogType) SpecExtensions(session.Spec) []string { return t.Extensions() }

// NeedsCredential: only a REST catalog with a bearer token or OAuth2 client
// credentials signs in. s3_tables signs with the storage connection's secret.
// A spec that does not say its kind yet (the type-only question of
// connections.NeedsCredential) answers true: the type can need one.
func (icebergCatalogType) NeedsCredential(s session.Spec) bool {
	switch opt(s, "endpoint_type") {
	case endpointS3Tables:
		return false
	case endpointREST:
		return icebergAuth(s) != authNone
	}
	return true
}

// Delegation is the ACCESS_DELEGATION_MODE an attach asks the catalog for.
type Delegation string

const (
	// DelegationNone: the catalog vends no credentials; data is read with the
	// person's own storage connections and fails closed elsewhere.
	DelegationNone Delegation = "none"
	// DelegationVended: the catalog's per-table vended credentials sign the
	// data reads (their scope and endpoint are the catalog's choice).
	DelegationVended Delegation = "vended_credentials"
)

// delegationParam carries an explicit Delegation in Spec.Params. It has no
// "opt." prefix, so a definition's options can never set it (Def.Params only
// writes known keys and opt.*): only code that builds a Spec can.
const delegationParam = "attach.delegation"

// WithDelegation returns s with the ACCESS_DELEGATION_MODE the attach must use.
// The lake reader (L4) uses DelegationVended for every catalog it attaches. The
// input's Params are not modified.
func WithDelegation(s session.Spec, d Delegation) session.Spec {
	p := make(map[string]string, len(s.Params)+1)
	for k, v := range s.Params {
		p[k] = v
	}
	p[delegationParam] = string(d)
	s.Params = p
	return s
}

// DelegationOf is the mode AttachSQL writes for s. Without an explicit
// WithDelegation it is what the main hub uses: DelegationVended for a catalog
// the person marked trust_writers, DelegationNone for every other, so an
// untrusted catalog never brings its own vended secrets into the main hub.
func DelegationOf(s session.Spec) (Delegation, error) {
	switch v := Delegation(s.Params[delegationParam]); v {
	case DelegationNone, DelegationVended:
		return v, nil
	case "":
		if trustsWriters(s) {
			return DelegationVended, nil
		}
		return DelegationNone, nil
	default:
		return "", fmt.Errorf("iceberg_catalog connection %q: %q is not an access delegation mode", s.Name, string(v))
	}
}

func trustsWriters(s session.Spec) bool { return opt(s, "trust_writers") == "true" }

const (
	endpointS3Tables = "s3_tables"
	endpointREST     = "rest"

	authOAuth2 = "oauth2"
	authBearer = "bearer"
	authNone   = "none"

	maxWarehouseLen = 200
	maxURILen       = 2048
)

var (
	icebergEndpointKinds = []string{endpointS3Tables, endpointREST}
	icebergAuths         = []string{authOAuth2, authBearer, authNone}
	// s3TablesARNRE is a table bucket ARN: arn:aws[-cn|-us-gov]:s3tables:<region>:<account>:bucket/<name>.
	s3TablesARNRE = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:s3tables:[a-z0-9-]{1,32}:[0-9]{12}:bucket/[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

// icebergAuth is the REST authentication, defaulting to oauth2.
func icebergAuth(s session.Spec) string {
	if a := opt(s, "auth"); a != "" {
		return a
	}
	return authOAuth2
}

func (t icebergCatalogType) Validate(s session.Spec) error {
	if err := noStrayFields(s); err != nil {
		return err
	}
	if err := unknownOptions(s, "endpoint_type", "warehouse", "storage", "uri", "auth", "oauth2_server_uri", "trust_writers"); err != nil {
		return err
	}
	if !s.ReadOnly {
		return fmt.Errorf("iceberg_catalog connection %q must be read-only (set read_only: true): Slaw does not write to an Iceberg catalog", s.Name)
	}
	if tw := opt(s, "trust_writers"); tw != "" && tw != "true" && tw != "false" {
		return fmt.Errorf("iceberg_catalog connection %q: trust_writers %q is not true or false", s.Name, tw)
	}
	kind := opt(s, "endpoint_type")
	switch kind {
	case "":
		return fmt.Errorf("iceberg_catalog connection %q needs an endpoint_type (%s)", s.Name, strings.Join(icebergEndpointKinds, " or "))
	case "glue":
		return fmt.Errorf("iceberg_catalog connection %q: endpoint_type glue is not supported yet (use %s)", s.Name, strings.Join(icebergEndpointKinds, " or "))
	case endpointS3Tables:
		return t.validateS3Tables(s)
	case endpointREST:
		return t.validateREST(s)
	}
	return fmt.Errorf("iceberg_catalog connection %q: endpoint_type %q is not one of %s", s.Name, kind, strings.Join(icebergEndpointKinds, ", "))
}

func (icebergCatalogType) validateS3Tables(s session.Spec) error {
	for _, o := range []string{"uri", "auth", "oauth2_server_uri"} {
		if opt(s, o) != "" {
			return fmt.Errorf("iceberg_catalog connection %q: option %q is only for a rest catalog (an s3_tables catalog signs with its storage connection)", s.Name, o)
		}
	}
	w := opt(s, "warehouse")
	if err := requireField(s, "a warehouse (the table bucket's ARN)", w); err != nil {
		return err
	}
	if !s3TablesARNRE.MatchString(w) {
		return fmt.Errorf("iceberg_catalog connection %q: warehouse must be a table bucket ARN such as arn:aws:s3tables:us-east-1:111122223333:bucket/name", s.Name)
	}
	st := opt(s, "storage")
	if err := requireField(s, "a storage connection (option storage: the s3 connection that signs it)", st); err != nil {
		return err
	}
	if err := connname.Validate(st); err != nil {
		return fmt.Errorf("iceberg_catalog connection %q: storage %q is not a connection name: %w", s.Name, st, err)
	}
	if st == s.Name {
		return fmt.Errorf("iceberg_catalog connection %q cannot be its own storage connection", s.Name)
	}
	return nil
}

func (icebergCatalogType) validateREST(s session.Spec) error {
	if opt(s, "storage") != "" {
		return fmt.Errorf("iceberg_catalog connection %q: option \"storage\" is only for an s3_tables catalog", s.Name)
	}
	w := opt(s, "warehouse")
	if err := requireField(s, "a warehouse (the catalog's warehouse name)", w); err != nil {
		return err
	}
	if utf8.RuneCountInString(w) > maxWarehouseLen {
		return fmt.Errorf("iceberg_catalog connection %q: warehouse is longer than %d characters", s.Name, maxWarehouseLen)
	}
	for _, r := range w {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("iceberg_catalog connection %q: warehouse contains a character that is not printable", s.Name)
		}
	}
	if err := requireField(s, "a uri (the catalog's base URL)", opt(s, "uri")); err != nil {
		return err
	}
	if err := validateCatalogURI(s, "uri", opt(s, "uri")); err != nil {
		return err
	}
	if err := oneOf("auth", opt(s, "auth"), icebergAuths...); err != nil {
		return err
	}
	if o := opt(s, "oauth2_server_uri"); o != "" {
		if icebergAuth(s) != authOAuth2 {
			return fmt.Errorf("iceberg_catalog connection %q: oauth2_server_uri is only valid with auth oauth2", s.Name)
		}
		return validateCatalogURI(s, "oauth2_server_uri", o)
	}
	return nil
}

// validateCatalogURI accepts an https URL, or an http URL for a loopback host
// only (so a token is never sent in clear to a remote host), with no user
// information, query or fragment: those would carry a credential or change
// what is requested.
func validateCatalogURI(s session.Spec, field, v string) error {
	bad := func(problem string) error {
		return fmt.Errorf("iceberg_catalog connection %q: %s %s", s.Name, field, problem)
	}
	if err := noControl(field, v); err != nil {
		return err
	}
	if len(v) > maxURILen || strings.ContainsAny(v, " \t\\") || strings.ContainsAny(v, "?#") {
		return bad("must be a plain URL with no spaces, query or fragment")
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return bad("is not a URL (want https://host/path)")
	}
	if p := u.Port(); p != "" {
		if n, perr := strconv.Atoi(p); perr != nil || n < 1 || n > 65535 {
			return bad("has a port outside 1-65535")
		}
	}
	if u.User != nil {
		return bad("must not carry a user name or password")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return bad("must not have a query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return bad("may use http only for localhost (a token would be sent in clear); use https")
		}
	default:
		return bad("must start with https://")
	}
	return nil
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (t icebergCatalogType) SecretSteps(s session.Spec, c secrets.Credential) ([]session.SecretStep, error) {
	if err := t.Validate(s); err != nil {
		return nil, err
	}
	if opt(s, "endpoint_type") != endpointREST {
		return nil, nil
	}
	step := session.SecretStep{Name: secretName(s.Name), Type: "iceberg"}
	switch icebergAuth(s) {
	case authNone:
		return nil, nil
	case authBearer:
		if c.Token == "" {
			return nil, fmt.Errorf("iceberg_catalog connection %q needs a bearer token", s.Name)
		}
		step.Params = []session.SecretParam{{Key: "TOKEN", Value: c.Token, Secret: true}}
	default:
		if c.KeyID == "" || c.Secret == "" {
			return nil, fmt.Errorf("iceberg_catalog connection %q needs an OAuth2 client id and secret", s.Name)
		}
		step.Params = []session.SecretParam{
			{Key: "CLIENT_ID", Value: c.KeyID},
			{Key: "CLIENT_SECRET", Value: c.Secret, Secret: true},
		}
		// DuckDB refuses an oauth2 secret that names neither a token URL nor an
		// endpoint to derive it from (<endpoint>/v1/oauth/tokens), so without
		// oauth2_server_uri the secret carries the catalog's own URI (a plain value).
		if o := opt(s, "oauth2_server_uri"); o != "" {
			step.Params = append(step.Params, session.SecretParam{Key: "OAUTH2_SERVER_URI", Value: o})
		} else {
			step.Params = append(step.Params, session.SecretParam{Key: "ENDPOINT", Value: opt(s, "uri")})
		}
	}
	return []session.SecretStep{step}, nil
}

// AttachSQL carries no credential: a REST catalog's token or client secret is in
// its secret, and an s3_tables catalog names its storage connection's secret.
func (t icebergCatalogType) AttachSQL(s session.Spec) ([]string, error) {
	if err := t.Validate(s); err != nil {
		return nil, err
	}
	mode, err := DelegationOf(s)
	if err != nil {
		return nil, err
	}
	var opts []string
	if opt(s, "endpoint_type") == endpointS3Tables {
		opts = []string{
			"TYPE iceberg",
			"ENDPOINT_TYPE " + session.QuoteLiteral(endpointS3Tables),
			"SECRET " + session.QuoteLiteral(secretName(opt(s, "storage"))),
		}
	} else {
		opts = []string{"TYPE iceberg", "ENDPOINT " + session.QuoteLiteral(opt(s, "uri"))}
		if icebergAuth(s) == authNone {
			opts = append(opts, "AUTHORIZATION_TYPE "+session.QuoteLiteral(authNone))
		} else {
			opts = append(opts, "SECRET "+session.QuoteLiteral(secretName(s.Name)))
		}
	}
	opts = append(opts, "ACCESS_DELEGATION_MODE "+session.QuoteLiteral(string(mode)), "READ_ONLY")
	return []string{"ATTACH " + session.QuoteLiteral(opt(s, "warehouse")) + " AS " + session.QuoteIdent(s.Name) +
		" (" + strings.Join(opts, ", ") + ")"}, nil
}

const (
	// probeNamespaceCap bounds the namespaces a Test lists.
	probeNamespaceCap = 50
	// probeTableCap bounds the table names a Test reads (names only: a table's
	// columns need a LoadTable each).
	probeTableCap = 1000
)

// Probe lists namespaces and table names (neither loads a table: S0 item 10),
// then describes one table, the one LoadTable a Test makes.
//
// TODO(L3): report where up to 5 tables keep their data, run through
// workspace.StorageFor, so the form can say which storage connection reads it.
// DuckDB 1.5 offers no cheap way to get a table's location from the attached
// catalog, and the person-only test path (iceberg_metadata, or the REST
// LoadTable response) needs the L3 resolution that knows the storage
// connections.
func (icebergCatalogType) Probe(ctx context.Context, sess session.Session, s session.Spec) (int, string, error) {
	if opt(s, "endpoint_type") == endpointS3Tables && len(s.Requires) == 0 {
		return 0, fmt.Sprintf("signs with %s: set it up first", opt(s, "storage")), nil
	}
	db := sess.DB()
	var namespaces []string
	rows, err := db.QueryContext(ctx, `SELECT schema_name FROM duckdb_schemas() WHERE database_name = $1 ORDER BY schema_name LIMIT $2`, s.Name, probeNamespaceCap+1)
	if err != nil {
		return 0, "", fmt.Errorf("attached, but listing namespaces failed: %w", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return 0, "", fmt.Errorf("attached, but listing namespaces failed: %w", err)
		}
		namespaces = append(namespaces, n)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, "", fmt.Errorf("attached, but listing namespaces failed: %w", err)
	}
	_ = rows.Close()
	nsDetail := fmt.Sprintf("%d namespaces", len(namespaces))
	if len(namespaces) > probeNamespaceCap {
		namespaces = namespaces[:probeNamespaceCap]
		nsDetail = fmt.Sprintf("%d+ namespaces", probeNamespaceCap)
	}
	tables, err := listTableNames(ctx, db, s.Name, namespaces)
	if err != nil {
		return 0, "", err
	}
	detail := fmt.Sprintf("%s, %d tables", nsDetail, len(tables))
	if len(tables) == 0 {
		return 0, detail, nil
	}
	first := tables[0]
	var cols int
	q := `SELECT count(*) FROM (DESCRIBE SELECT * FROM ` + session.QuoteQualified(s.Name, first[0], first[1]) + `)`
	if err := db.QueryRowContext(ctx, q).Scan(&cols); err != nil {
		return 0, "", fmt.Errorf("attached, but reading the schema of %s.%s failed: %w", first[0], first[1], err)
	}
	return len(tables), fmt.Sprintf("%s; %s: %d columns", detail, first[1], cols), nil
}

// listTableNames returns (namespace, table) for the tables of the first
// namespaces, at most probeTableCap, in name order. Only names are read.
func listTableNames(ctx context.Context, db session.Querier, catalog string, namespaces []string) ([][2]string, error) {
	if len(namespaces) == 0 {
		return nil, nil
	}
	var out [][2]string
	for _, ns := range namespaces {
		rows, err := db.QueryContext(ctx, `SELECT table_name FROM duckdb_tables() WHERE database_name = $1 AND schema_name = $2 ORDER BY table_name LIMIT $3`,
			catalog, ns, probeTableCap-len(out))
		if err != nil {
			return nil, fmt.Errorf("attached, but listing tables failed: %w", err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("attached, but listing tables failed: %w", err)
			}
			out = append(out, [2]string{ns, n})
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("attached, but listing tables failed: %w", err)
		}
		if len(out) >= probeTableCap {
			break
		}
	}
	return out, nil
}

func (icebergCatalogType) Info() TypeInfo {
	return TypeInfo{
		Name: "iceberg_catalog", Label: "Iceberg catalog (S3 Tables, REST)",
		Fields: []Field{
			{Name: "endpoint_type", Label: "Catalog kind", Kind: KindEnum, Choices: append([]string(nil), icebergEndpointKinds...), Required: true, Option: true, Prominent: true,
				Help: "s3_tables is Amazon S3 Tables; rest is an Iceberg REST catalog (Polaris, Nessie, Lakekeeper, Unity, ...). Glue is not supported yet."},
			{Name: "warehouse", Label: "Warehouse", Kind: KindText, Required: true, Option: true,
				Help: "s3_tables: the table bucket's ARN (arn:aws:s3tables:REGION:ACCOUNT:bucket/NAME). rest: the catalog's warehouse name."},
			{Name: "storage", Label: "Signs with (S3 connection)", Kind: KindText, Option: true,
				Help: "s3_tables only: the name of the s3 connection whose identity signs the catalog. Its scope should cover the table bucket's data."},
			{Name: "uri", Label: "Catalog URL", Kind: KindURL, Option: true,
				Help: "rest only: the catalog's base URL. https, or http for localhost only; no user name, query or fragment."},
			{Name: "auth", Label: "Authentication", Kind: KindEnum, Choices: append([]string(nil), icebergAuths...), Default: authOAuth2, Option: true,
				Help: "rest only: oauth2 (client id and secret), bearer (a token) or none."},
			{Name: "oauth2_server_uri", Label: "OAuth2 token URL", Kind: KindURL, Option: true,
				Help: "rest with oauth2 only: where the client id and secret are exchanged for a token. Leave empty for the catalog's own."},
			{Name: "trust_writers", Label: "Trust this catalog's writers", Kind: KindEnum, Choices: []string{"true", "false"}, Default: "false", Option: true,
				Help: "Off: the agent reads this catalog only in a confined reader, on its own. On: the agent reads it with your own access and can join it with other data, and the catalog's vended credentials are used here. " +
					"The risk, in plain words: a trusted catalog's operator and writers can change where your own S3 reads go and the answers they get, and can make agent queries read local files and loopback http. " +
					"Turn it on only for catalogs whose operator and writers you would trust with your machine."},
		},
		Credentials: []Field{
			{Name: "token", Label: "Bearer token", Kind: KindText, Secret: true, PerPerson: true, Help: "rest with auth bearer"},
			{Name: "key_id", Label: "OAuth2 client id", Kind: KindText, PerPerson: true, Help: "rest with auth oauth2"},
			{Name: "secret", Label: "OAuth2 client secret", Kind: KindText, Secret: true, PerPerson: true, Help: "rest with auth oauth2"},
		},
		Extensions:        icebergCatalogType{}.Extensions(),
		Capabilities:      Capabilities{Catalog: true, Writable: false, ReadOnlyAttach: true, Remote: true, Test: true},
		FingerprintFields: []string{"type", "option:endpoint_type", "option:warehouse", "option:uri", "option:trust_writers"},
		SignInMethods:     []string{"keychain", "env", "prompt"},
	}
}
