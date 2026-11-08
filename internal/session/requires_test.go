package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SandwichLabs/slaw/internal/secrets"
)

// lakeyType stands in for a lake: it attaches an in-memory catalog and records
// whether the "stub_store" secret already existed when it did.
type lakeyType struct {
	sawSecret *atomic.Bool
	// bare counts attaches made while the storage secret did not exist.
	bare *atomic.Int32
	// hook, when set, runs inside PreAttach (before the ATTACH).
	hook func()
}

func (lakeyType) Name() string         { return "lakey" }
func (lakeyType) Remote() bool         { return false }
func (lakeyType) Validate(Spec) error  { return nil }
func (lakeyType) Extensions() []string { return nil }
func (l lakeyType) AttachSQL(sp Spec) ([]string, error) {
	return []string{"ATTACH ':memory:' AS " + QuoteIdent(sp.Name)}, nil
}
func (l lakeyType) PreAttach(ctx context.Context, conn QueryExecer, _ Spec) error {
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_secrets() WHERE name = 'stub_store'").Scan(&n); err != nil {
		return err
	}
	if l.hook != nil {
		l.hook()
	}
	l.sawSecret.Store(n == 1)
	if n != 1 && l.bare != nil {
		l.bare.Add(1)
	}
	return nil
}

func requiresSession(t *testing.T, ext string) (Session, *atomic.Bool) {
	saw := &atomic.Bool{}
	return newTestSession(t, WithConnectionTypes(stubType{ext: ext}, lakeyType{sawSecret: saw}, tblType{})), saw
}

// tblType stands in for a Delta or Iceberg location: catalog-less, no secret of
// its own, connected at preflight.
type tblType struct{}

func (tblType) Name() string                                               { return "tbl" }
func (tblType) Remote() bool                                               { return true }
func (tblType) Validate(Spec) error                                        { return nil }
func (tblType) Extensions() []string                                       { return nil }
func (tblType) NeedsCredential(Spec) bool                                  { return false }
func (tblType) Catalogless() bool                                          { return true }
func (tblType) SecretSteps(Spec, secrets.Credential) ([]SecretStep, error) { return nil, nil }
func (tblType) AttachSQL(Spec) ([]string, error)                           { return []string{}, nil }

// switchable is a storage credential that can be present or absent.
type switchable struct{ have atomic.Bool }

func (c *switchable) fn(context.Context) (secrets.Credential, error) {
	if !c.have.Load() {
		return secrets.Credential{}, &secrets.ErrNeedsCredentials{Method: "keychain", Reason: "not set"}
	}
	return secrets.Credential{Token: "tok"}, nil
}

func secretCount(t *testing.T, s Session) int {
	t.Helper()
	var n int
	require.NoError(t, s.DB().QueryRow("SELECT count(*) FROM duckdb_secrets() WHERE name = 'stub_store'").Scan(&n))
	return n
}

func attachedCatalogs(t *testing.T, s Session, name string) int {
	t.Helper()
	var n int
	require.NoError(t, s.DB().QueryRow("SELECT count(*) FROM duckdb_databases() WHERE database_name = ?", name).Scan(&n))
	return n
}

// A table source (catalog-less) registered and preflighted before its storage
// connection is registered, or while it needs credentials, recovers on its
// own once the storage connection is ready.
func TestRequiresCataloglessDependentRecovers(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	cred := &switchable{}
	require.NoError(t, s.Register(Spec{Name: "events", Type: "tbl", Requires: []string{"store"}}))
	s.Preflight(ctx, "events") // the storage connection is not even registered yet
	assert.Equal(t, `needs the "store" connection: it is not defined`, status(t, s, "events").Message)

	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
	s.Preflight(ctx, "store")
	s.Preflight(ctx, "events")
	assert.Equal(t, `needs the "store" connection: it needs credentials`, status(t, s, "events").Message)

	cred.have.Store(true)
	s.Preflight(ctx, "store") // the person signs in; nothing else is done
	assert.Equal(t, StatusOK, status(t, s, "store").Kind)
	assert.Equal(t, StatusOK, status(t, s, "events").Kind)
}

// A lake that waited on a missing credential goes back to not connected when
// the storage connection becomes ready (no stale dependency error).
func TestRequiresDependentErrorClearsWhenStorageIsReady(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	cred := &switchable{}
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
	require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
	require.Error(t, s.Connect(ctx, "lake"))
	assert.Equal(t, StatusNeedsCredentials, status(t, s, "lake").Kind, "waiting on a sign-in is a sign-in to do, not an error")
	cred.have.Store(true)
	s.Preflight(ctx, "store")
	assert.Equal(t, StatusNotConnected, status(t, s, "lake").Kind)
	require.NoError(t, s.Connect(ctx, "lake"))
}

// An attached lake never outlives its storage secret: dropping the secret
// (credential removed, Preflight, Disconnect, Detach) detaches it, so nothing
// reads its URLs without a secret.
func TestRequiresAttachedDependentIsDetachedWithTheSecret(t *testing.T) {
	ctx := context.Background()
	for _, how := range []string{"credential removed", "detach"} {
		t.Run(how, func(t *testing.T) {
			s, _ := requiresSession(t, "json")
			cred := &switchable{}
			cred.have.Store(true)
			require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
			require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
			require.NoError(t, s.Connect(ctx, "lake"))
			require.Equal(t, 1, attachedCatalogs(t, s, "lake"))

			if how == "detach" {
				require.NoError(t, s.Detach(ctx, "store"))
			} else {
				cred.have.Store(false)
				s.Preflight(ctx, "store")
				assert.Equal(t, StatusNeedsCredentials, status(t, s, "store").Kind)
			}
			assert.Zero(t, secretCount(t, s))
			assert.Zero(t, attachedCatalogs(t, s, "lake"), "the lake is detached with the secret")
			assert.Equal(t, StatusNotConnected, status(t, s, "lake").Kind)
			_, err := s.DB().Exec("SELECT * FROM lake.main.t")
			require.Error(t, err)

			err = s.ConnectReferenced(ctx, "", "SELECT * FROM lake.main.t")
			require.Error(t, err)
			msg := status(t, s, "lake").Message
			if how == "detach" {
				assert.Equal(t, `needs the "store" connection: it is not defined`, msg)
			} else {
				assert.Equal(t, `needs the "store" connection: it needs credentials`, msg)
			}
		})
	}
}

// The storage secret is dropped (credential removed, Preflight) while the lake
// connects: the lake must never attach with no secret. Run many times.
func TestRequiresNeverAttachesWithoutTheSecret(t *testing.T) {
	ctx := context.Background()
	saw := &atomic.Bool{}
	bare := &atomic.Int32{}
	s := newTestSession(t, WithConnectionTypes(stubType{ext: "json"}, lakeyType{sawSecret: saw, bare: bare}))
	cred := &switchable{}
	cred.have.Store(true)
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
	for i := 0; i < 60; i++ {
		require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
		cred.have.Store(true)
		s.Preflight(ctx, "store")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.Connect(ctx, "lake") }()
		go func() {
			defer wg.Done()
			cred.have.Store(false)
			s.Preflight(ctx, "store")
		}()
		wg.Wait()
		if secretCount(t, s) == 0 {
			assert.Zero(t, attachedCatalogs(t, s, "lake"), "a lake is attached with no storage secret")
			assert.NotEqual(t, StatusOK, status(t, s, "lake").Kind)
		}
		_ = s.Detach(ctx, "lake")
	}
	assert.Zero(t, bare.Load(), "the lake attached while the secret did not exist")
}

// An attached lake keeps its status when a later connect attempt finds a
// requirement missing.
func TestRequiresDoesNotTouchAnAttachedDependentStatus(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: credOK("tok")}))
	require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
	require.NoError(t, s.Connect(ctx, "lake"))
	h, ok := s.(*hub)
	require.True(t, ok)
	require.Error(t, h.requirementError("lake", "store", "it needs credentials"))
	assert.Equal(t, StatusOK, status(t, s, "lake").Kind)
}

func TestRequiresConnectsStorageFirst(t *testing.T) {
	ctx := context.Background()
	s, saw := requiresSession(t, "json")
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: credOK("tok")}))
	require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
	require.NoError(t, s.Connect(ctx, "lake"))
	assert.True(t, saw.Load(), "the storage secret existed before the lake attached")
	assert.Equal(t, StatusOK, status(t, s, "store").Kind)
	assert.Equal(t, StatusOK, status(t, s, "lake").Kind)
}

func TestRequiresReferencedByQueryConnectsStorage(t *testing.T) {
	s, saw := requiresSession(t, "json")
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: credOK("tok")}))
	require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
	require.NoError(t, s.ConnectReferenced(context.Background(), "", "SELECT 1 FROM lake.main.t"))
	assert.True(t, saw.Load())
}

// A requirement that needs the person to sign in leaves the dependent needing
// credentials too (the same state, the same hint); any other reason is an error.
func TestRequiresNotReadyIsAStatusWithAPhrase(t *testing.T) {
	ctx := context.Background()
	tests := map[string]struct {
		ext  string
		cred CredentialFunc
		want string
		kind StatusKind
	}{
		"needs credentials": {"json", func(context.Context) (secrets.Credential, error) {
			return secrets.Credential{}, &secrets.ErrNeedsCredentials{Method: "keychain", Reason: "not set"}
		}, `needs the "store" connection: it needs credentials`, StatusNeedsCredentials},
		"no credential func": {"json", nil, `needs the "store" connection: it needs credentials`, StatusNeedsCredentials},
		"extension missing":  {"no_such_extension", credOK("tok"), `needs the "store" connection: its DuckDB extension is not installed`, StatusError},
		"error": {"json", func(context.Context) (secrets.Credential, error) {
			return secrets.Credential{}, errors.New("boom tok_secret_needle")
		}, `needs the "store" connection: it could not connect`, StatusError},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s, _ := requiresSession(t, tc.ext)
			require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: tc.cred}))
			require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
			err := s.Connect(ctx, "lake")
			require.Error(t, err)
			st := status(t, s, "lake")
			assert.Equal(t, tc.kind, st.Kind)
			assert.Equal(t, tc.want, st.Message)
			assert.NotContains(t, st.Message+err.Error(), "tok_secret_needle")
		})
	}
}

func TestRequiresUnknownSelfAndCycle(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	require.NoError(t, s.Register(Spec{Name: "a", Type: "lakey", Requires: []string{"ghost"}}))
	require.Error(t, s.Connect(ctx, "a"))
	assert.Equal(t, `needs the "ghost" connection: it is not defined`, status(t, s, "a").Message)

	require.NoError(t, s.Register(Spec{Name: "me", Type: "lakey", Requires: []string{"me"}}))
	require.Error(t, s.Connect(ctx, "me"))
	assert.Contains(t, status(t, s, "me").Message, "depend on itself")

	require.NoError(t, s.Register(Spec{Name: "x", Type: "lakey", Requires: []string{"y"}}))
	require.NoError(t, s.Register(Spec{Name: "y", Type: "lakey", Requires: []string{"x"}}))
	require.Error(t, s.Connect(ctx, "x"))
	assert.Equal(t, StatusError, status(t, s, "x").Kind)
}

// A requirement that was not ready is not remembered as the dependent's own
// failure: once it is fixed the next implicit connect works at once.
func TestRequiresFixedLaterConnectsOnFirstUse(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	var have atomic.Bool
	cred := func(context.Context) (secrets.Credential, error) {
		if !have.Load() {
			return secrets.Credential{}, &secrets.ErrNeedsCredentials{Method: "keychain", Reason: "not set"}
		}
		return secrets.Credential{Token: "tok"}, nil
	}
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred}))
	require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
	require.Error(t, s.ConnectReferenced(ctx, "lake", ""))
	have.Store(true)
	s.Preflight(ctx, "store")
	require.NoError(t, s.ConnectReferenced(ctx, "lake", ""))
	assert.Equal(t, StatusOK, status(t, s, "lake").Kind)
}

// Lake and storage connecting at the same moment, from many goroutines.
func TestRequiresConcurrentConnectsDoNotDeadlock(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: credOK("tok")}))
	for _, n := range []string{"l1", "l2", "l3"} {
		require.NoError(t, s.Register(Spec{Name: n, Type: "lakey", Requires: []string{"store"}}))
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		for _, n := range []string{"l1", "l2", "l3", "store"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NoError(t, s.Connect(ctx, n))
			}()
		}
	}
	wg.Wait()
	for _, n := range []string{"l1", "l2", "l3", "store"} {
		assert.Equal(t, StatusOK, status(t, s, n).Kind)
	}
}

// A Preflight that read the storage entry before it was detached and
// registered again must not drop the new entry's secret: the new entry would
// stay attached with none, and a lake would attach bare (the #124 stress flake).
func TestRequiresStalePreflightKeepsTheNewStorageSecret(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	cred := &switchable{}
	cred.have.Store(true)
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
	require.NoError(t, s.Connect(ctx, "store"))
	h, ok := s.(*hub)
	require.True(t, ok)
	stale, att := h.entryOf("store") // what a Preflight read before the swap
	require.True(t, att)

	require.NoError(t, s.Detach(ctx, "store"))
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
	require.NoError(t, s.Connect(ctx, "store"))
	require.Equal(t, 1, secretCount(t, s))

	h.preflightEntry(ctx, "store", stale, true)

	assert.Equal(t, 1, secretCount(t, s), "the new storage entry keeps its secret")
	_, att = h.entryOf("store")
	assert.True(t, att)
}

// Register waits for a connect in progress: it cannot swap the entry between
// connectLocked's re-read and its attach, which would leave the lake attached
// in DuckDB under an orphaned entry that no secret change can reach.
func TestRequiresRegisterWaitsForAConnectInProgress(t *testing.T) {
	ctx := context.Background()
	inPre, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func() {
		once.Do(func() { close(inPre); <-release })
	}
	s := newTestSession(t, WithConnectionTypes(stubType{ext: "json"}, lakeyType{sawSecret: &atomic.Bool{}, hook: hook}))
	cred := &switchable{}
	cred.have.Store(true)
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
	require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))

	connected := make(chan error, 1)
	go func() { connected <- s.Connect(ctx, "lake") }()
	<-inPre
	registered := make(chan error, 1)
	go func() { registered <- s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}) }()
	select {
	case <-registered:
		t.Fatal("Register returned while the lake was connecting")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-connected)
	assert.Error(t, <-registered, "the lake is attached by now, so it cannot be registered over")

	cred.have.Store(false)
	s.Preflight(ctx, "store")
	assert.Zero(t, secretCount(t, s))
	assert.Zero(t, attachedCatalogs(t, s, "lake"), "no lake catalog outlives the storage secret")
}

// A dependent waiting on a signed-out connection says which one (BlockedBy) and
// carries no sign-in hint of its own: the prompt and the banner are the
// requirement's, and the revive pass (which re-reads hinted statuses) never
// touches the dependent.
func TestRequiresDependentNamesTheBlockerAndCarriesNoSignInHint(t *testing.T) {
	ctx := context.Background()
	s, _ := requiresSession(t, "json")
	hint := &secrets.SignInHint{Kind: "aws-sso", Subject: "dev", Reason: "missing"}
	require.NoError(t, s.Register(Spec{Name: "store", Type: "stub", Credential: func(context.Context) (secrets.Credential, error) {
		return secrets.Credential{}, &secrets.ErrNeedsCredentials{Method: "aws-sso:dev", SignIn: hint}
	}}))
	require.NoError(t, s.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
	require.Error(t, s.Connect(ctx, "lake"))

	lake := status(t, s, "lake")
	assert.Equal(t, StatusNeedsCredentials, lake.Kind)
	assert.Equal(t, "store", lake.BlockedBy)
	assert.Nil(t, lake.SignIn, "the sign-in is the blocker's, not the dependent's")
	store := status(t, s, "store")
	require.NotNil(t, store.SignIn)
	assert.Equal(t, "dev", store.SignIn.Subject)
	assert.Empty(t, store.BlockedBy)

	// reviveSignedOut picks up the blocker only.
	h, isHub := s.(*hub)
	require.True(t, isHub)
	h.mu.Lock()
	var hinted []string
	for n, e := range h.conns {
		if e.status.Kind == StatusNeedsCredentials && e.status.SignIn != nil {
			hinted = append(hinted, n)
		}
	}
	h.mu.Unlock()
	assert.Equal(t, []string{"store"}, hinted)

	// Once the requirement is ready the dependent forgets it.
	s2, _ := requiresSession(t, "json")
	cred := &switchable{}
	require.NoError(t, s2.Register(Spec{Name: "store", Type: "stub", Credential: cred.fn}))
	require.NoError(t, s2.Register(Spec{Name: "lake", Type: "lakey", Requires: []string{"store"}}))
	require.Error(t, s2.Connect(ctx, "lake"))
	assert.Equal(t, "store", status(t, s2, "lake").BlockedBy)
	cred.have.Store(true)
	s2.Preflight(ctx, "store")
	assert.Empty(t, status(t, s2, "lake").BlockedBy)
}
