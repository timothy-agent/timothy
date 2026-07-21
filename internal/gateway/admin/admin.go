// Package admin is the gateway's control plane: provider and route
// CRUD over the same tables the router loads (D-004 — providers are
// data), every mutation audited and followed by an in-process snapshot
// reload so changes serve without restarts.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/internal/secretstore"
)

// pgxQuerier is satisfied by both *pgxpool.Pool and pgx.Tx: scanProvider
// runs the same query whether or not it's inside a locking transaction.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// drivers whitelists what the gateway can actually construct. CLI
// drivers arrive in a later phase; the panel shows them disabled.
var drivers = map[string]bool{"anthropic": true, "openaicompat": true, "bedrock": true}

// credentialRefPattern accepts names and paths (env var names, Vault
// paths, AWS profile names) and rejects anything that could be a
// pasted secret: no spaces, no long opaque blobs.
var credentialRefPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]{0,128}$`)

const testTimeout = 20 * time.Second

// Admin mutates routing configuration. store reloads the serving
// snapshot after every write; rec books test-connection probes under
// purpose='test' so they never pollute usage.
type Admin struct {
	db      *pgpool.Pool
	store   *router.Store
	rec     ledger.Recorder
	budgets *ledger.BudgetStore
	secrets *secretstore.Store
	log     *slog.Logger
}

func New(db *pgpool.Pool, store *router.Store, rec ledger.Recorder, budgets *ledger.BudgetStore, secrets *secretstore.Store, log *slog.Logger) *Admin {
	return &Admin{db: db, store: store, rec: rec, budgets: budgets, secrets: secrets, log: log}
}

// SetSecret stores value under refName (write-only: the value is never
// read back through any admin endpoint) and reloads the serving
// snapshot so a provider whose credential_ref matches starts resolving
// immediately.
func (a *Admin) SetSecret(ctx context.Context, refName, value string) error {
	if refName == "" {
		return fmt.Errorf("ref name is required")
	}
	if value == "" {
		return fmt.Errorf("value is required")
	}
	if err := a.secrets.Set(ctx, refName, value); err != nil {
		return err
	}
	a.audit(ctx, "set", "secret", refName, nil, map[string]bool{"configured": true})
	a.reload(ctx)
	return nil
}

// DeleteSecret removes a stored secret value.
func (a *Admin) DeleteSecret(ctx context.Context, refName string) error {
	if err := a.secrets.Delete(ctx, refName); err != nil {
		return err
	}
	a.audit(ctx, "delete", "secret", refName, map[string]bool{"configured": true}, nil)
	a.reload(ctx)
	return nil
}

// SetSecretExternal points refName at a secret held in an external
// backend (vault, asm) instead of storing a value. backendRef is the
// path/name in that system.
func (a *Admin) SetSecretExternal(ctx context.Context, refName, backend, backendRef string) error {
	if refName == "" {
		return fmt.Errorf("ref name is required")
	}
	if err := a.secrets.SetExternal(ctx, refName, backend, backendRef); err != nil {
		return err
	}
	a.audit(ctx, "set", "secret", refName, nil, map[string]string{"backend": backend})
	a.reload(ctx)
	return nil
}

// SecretStatus reports whether refName has a stored secret and which
// backend serves it, without exposing the value — used to render the
// "configured" badge in the UI.
func (a *Admin) SecretStatus(ctx context.Context, refName string) (configured bool, backend string, err error) {
	if refName == "" {
		return false, "", nil
	}
	return a.secrets.Status(ctx, refName)
}

// SecretBackendConfig returns a backend's stored connection config
// (never a credential — the vault token lives in the secret store).
func (a *Admin) SecretBackendConfig(ctx context.Context, backend string) (json.RawMessage, error) {
	return a.secrets.GetBackendConfig(ctx, backend)
}

// SetSecretBackendConfig saves a backend's connection config and
// reloads the snapshot so refs served by that backend re-resolve. The
// audit row records the normalized config the store kept, not the raw
// request body — unknown fields (a mistakenly pasted token, say) must
// not survive anywhere.
func (a *Admin) SetSecretBackendConfig(ctx context.Context, backend string, cfg json.RawMessage) error {
	normalized, err := a.secrets.SetBackendConfig(ctx, backend, cfg)
	if err != nil {
		return err
	}
	a.audit(ctx, "set", "secret_backend", backend, nil, normalized)
	a.reload(ctx)
	return nil
}

// DeleteSecretBackendConfig removes a backend's connection config;
// secrets pointed at it fail to resolve until it's reconfigured.
func (a *Admin) DeleteSecretBackendConfig(ctx context.Context, backend string) error {
	if err := a.secrets.DeleteBackendConfig(ctx, backend); err != nil {
		return err
	}
	a.audit(ctx, "delete", "secret_backend", backend, nil, nil)
	a.reload(ctx)
	return nil
}

// TestSecretBackend checks connectivity and auth for an external
// secret backend without reading any stored secret.
func (a *Admin) TestSecretBackend(ctx context.Context, backend string) error {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	return a.secrets.TestBackend(ctx, backend)
}

// PatchBudget applies per-window budget changes: a key maps a window
// scope to its new USD limit, nil clears it, absent keys stay
// untouched. All keys are validated before any write so a bad entry
// cannot leave a partial update. No snapshot reload: budgets never
// affect routing.
func (a *Admin) PatchBudget(ctx context.Context, patch map[string]*float64) error {
	for scope, limit := range patch {
		if scope != "day" && scope != "month" {
			return fmt.Errorf("unknown budget scope %q", scope)
		}
		if limit != nil && *limit <= 0 {
			return fmt.Errorf("budget limit for %s must be positive", scope)
		}
	}
	before, err := a.budgets.Limits(ctx)
	if err != nil {
		return err
	}
	for scope, limit := range patch {
		if err := a.budgets.Set(ctx, scope, limit); err != nil {
			return err
		}
	}
	after, err := a.budgets.Limits(ctx)
	if err != nil {
		return err
	}
	a.audit(ctx, "update", "budget", "spend", before, after)
	return nil
}

// Provider is the API shape of one providers row. credential_ref is a
// NAME (env var / Vault path / AWS profile) — secret values are never
// stored or returned anywhere in this system.
type Provider struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Kind          string             `json:"kind"`
	Driver        string             `json:"driver"`
	BaseURL       string             `json:"base_url"`
	DefaultModel  string             `json:"default_model"`
	Models        []router.ModelInfo `json:"models"`
	CredentialRef string             `json:"credential_ref"`
	Headers       map[string]string  `json:"headers"`
	Enabled       bool               `json:"enabled"`
}

func validateProvider(p Provider) error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if p.Kind != "api" && p.Kind != "cli" {
		return fmt.Errorf("kind must be api or cli")
	}
	if p.Kind == "cli" {
		return fmt.Errorf("cli providers arrive in a later phase")
	}
	if !drivers[p.Driver] {
		return fmt.Errorf("unknown driver %q", p.Driver)
	}
	if !credentialRefPattern.MatchString(p.CredentialRef) {
		return fmt.Errorf("credential_ref must be a name or path (env var, Vault path, AWS profile), never a secret value")
	}
	return nil
}

// List returns every provider row, config order by name.
func (a *Admin) List(ctx context.Context) ([]Provider, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("admin providers: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, name, kind, driver, base_url, default_model,
		models, credential_ref, headers, enabled FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("admin providers: %w", err)
	}
	defer rows.Close()

	out := []Provider{}
	for rows.Next() {
		var (
			p            Provider
			models, hdrs []byte
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Driver, &p.BaseURL,
			&p.DefaultModel, &models, &p.CredentialRef, &hdrs, &p.Enabled); err != nil {
			return nil, fmt.Errorf("admin providers: %w", err)
		}
		if err := json.Unmarshal(models, &p.Models); err != nil {
			return nil, fmt.Errorf("admin providers: models: %w", err)
		}
		if err := json.Unmarshal(hdrs, &p.Headers); err != nil {
			return nil, fmt.Errorf("admin providers: headers: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Create inserts a provider (disabled by default is the caller's
// choice), audits, and reloads the snapshot.
func (a *Admin) Create(ctx context.Context, p Provider) (string, error) {
	if err := validateProvider(p); err != nil {
		return "", err
	}
	db, err := a.db.Get()
	if err != nil {
		return "", fmt.Errorf("admin create: %w", err)
	}
	models, hdrs := jsonOr(p.Models, "[]"), jsonOr(p.Headers, "{}")
	var id string
	err = db.QueryRow(ctx, `INSERT INTO providers
			(name, kind, driver, base_url, default_model, models, credential_ref, headers, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		p.Name, p.Kind, p.Driver, p.BaseURL, p.DefaultModel, models, p.CredentialRef, hdrs, p.Enabled).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("admin create: %w", err)
	}
	a.audit(ctx, "create", "provider", id, nil, p)
	a.reload(ctx)
	return id, nil
}

// Patch applies a partial update. Only fields present in the request
// change; before/after land in the audit row.
type ProviderPatch struct {
	BaseURL       *string             `json:"base_url"`
	DefaultModel  *string             `json:"default_model"`
	Models        *[]router.ModelInfo `json:"models"`
	CredentialRef *string             `json:"credential_ref"`
	Headers       *map[string]string  `json:"headers"`
	Enabled       *bool               `json:"enabled"`
}

func (a *Admin) Patch(ctx context.Context, id string, patch ProviderPatch) error {
	if patch.CredentialRef != nil && !credentialRefPattern.MatchString(*patch.CredentialRef) {
		return fmt.Errorf("credential_ref must be a name or path, never a secret value")
	}

	db, err := a.db.Get()
	if err != nil {
		return fmt.Errorf("admin patch: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("admin patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE holds the row for the whole read-modify-write: a
	// concurrent Patch blocks until this one commits, instead of both
	// reading the same before and one silently clobbering the other.
	before, err := a.getForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	after := before
	if patch.BaseURL != nil {
		after.BaseURL = *patch.BaseURL
	}
	if patch.DefaultModel != nil {
		after.DefaultModel = *patch.DefaultModel
	}
	if patch.Models != nil {
		after.Models = *patch.Models
	}
	if patch.CredentialRef != nil {
		after.CredentialRef = *patch.CredentialRef
	}
	if patch.Headers != nil {
		after.Headers = *patch.Headers
	}
	if patch.Enabled != nil {
		after.Enabled = *patch.Enabled
	}

	tag, err := tx.Exec(ctx, `UPDATE providers SET base_url = $2, default_model = $3,
			models = $4, credential_ref = $5, headers = $6, enabled = $7, updated_at = now()
		WHERE id = $1`,
		id, after.BaseURL, after.DefaultModel, jsonOr(after.Models, "[]"),
		after.CredentialRef, jsonOr(after.Headers, "{}"), after.Enabled)
	if err != nil {
		return fmt.Errorf("admin patch: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("provider %s: %w", id, ErrNotFound)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin patch: %w", err)
	}
	a.audit(ctx, "update", "provider", id, before, after)
	a.reload(ctx)
	return nil
}

// Delete removes a provider unless an enabled route still points at
// it — silent black holes in serving chains are worse than an error.
func (a *Admin) Delete(ctx context.Context, id string) error {
	db, err := a.db.Get()
	if err != nil {
		return fmt.Errorf("admin delete: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("admin delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := a.getForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}

	// task_routes isn't locked here, so a concurrent PatchRoute could
	// still commit a fresh reference right after this check reads zero.
	// PatchRoute's own provider-existence check runs inside its own
	// FOR UPDATE-guarded tx against this same row, so the two race
	// safely: whichever commits first wins, the other sees the updated
	// state and fails cleanly (delete finds a ref, or the route patch
	// finds the provider gone).
	var refs int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM task_routes
		WHERE enabled AND chain @> $1::jsonb`,
		fmt.Sprintf(`[{"provider_id": %q}]`, id)).Scan(&refs); err != nil {
		return fmt.Errorf("admin delete: %w", err)
	}
	if refs > 0 {
		return fmt.Errorf("provider %s is referenced by %d enabled route(s): %w", before.Name, refs, ErrInUse)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM providers WHERE id = $1`, id); err != nil {
		return fmt.Errorf("admin delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin delete: %w", err)
	}
	a.audit(ctx, "delete", "provider", id, before, nil)
	a.reload(ctx)
	return nil
}

// Route is the API shape of one task_routes row.
type Route struct {
	TaskCategory string              `json:"task_category"`
	Chain        []router.ChainEntry `json:"chain"`
	Enabled      bool                `json:"enabled"`
}

func (a *Admin) Routes(ctx context.Context) ([]Route, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("admin routes: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT task_category, chain, enabled FROM task_routes ORDER BY task_category`)
	if err != nil {
		return nil, fmt.Errorf("admin routes: %w", err)
	}
	defer rows.Close()

	out := []Route{}
	for rows.Next() {
		var (
			r     Route
			chain []byte
		)
		if err := rows.Scan(&r.TaskCategory, &chain, &r.Enabled); err != nil {
			return nil, fmt.Errorf("admin routes: %w", err)
		}
		if err := json.Unmarshal(chain, &r.Chain); err != nil {
			return nil, fmt.Errorf("admin routes: chain: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoutePatch reorders/replaces a category's chain and/or flips it.
type RoutePatch struct {
	Chain   *[]router.ChainEntry `json:"chain"`
	Enabled *bool                `json:"enabled"`
}

func (a *Admin) PatchRoute(ctx context.Context, category string, patch RoutePatch) error {
	db, err := a.db.Get()
	if err != nil {
		return fmt.Errorf("admin route patch: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("admin route patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		before    Route
		chainJSON []byte
	)
	err = tx.QueryRow(ctx, `SELECT task_category, chain, enabled FROM task_routes WHERE task_category = $1`,
		category).Scan(&before.TaskCategory, &chainJSON, &before.Enabled)
	if err != nil {
		return fmt.Errorf("route %s: %w", category, ErrNotFound)
	}
	_ = json.Unmarshal(chainJSON, &before.Chain)

	after := before
	if patch.Chain != nil {
		// Every entry must reference an existing provider — a typo'd id
		// becomes a silent skip at resolve time otherwise. FOR UPDATE
		// locks the referenced provider row against a concurrent Delete
		// racing to remove it after this check passes.
		for _, e := range *patch.Chain {
			var found string
			err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id = $1 FOR UPDATE`, e.ProviderID).Scan(&found)
			if err != nil {
				return fmt.Errorf("chain entry references unknown provider %s", e.ProviderID)
			}
		}
		after.Chain = *patch.Chain
	}
	if patch.Enabled != nil {
		after.Enabled = *patch.Enabled
	}

	if _, err := tx.Exec(ctx, `UPDATE task_routes SET chain = $2, enabled = $3, updated_at = now()
		WHERE task_category = $1`, category, jsonOr(after.Chain, "[]"), after.Enabled); err != nil {
		return fmt.Errorf("admin route patch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin route patch: %w", err)
	}
	a.audit(ctx, "update", "route", category, before, after)
	a.reload(ctx)
	return nil
}

// HealthRow is one provider's operational status: does its credential
// resolve, and when did it last succeed or fail in the ledger.
type HealthRow struct {
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Healthy     bool       `json:"healthy"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	LastError   *time.Time `json:"last_error,omitempty"`
}

func (a *Admin) Health(ctx context.Context) ([]HealthRow, error) {
	snap := a.store.Snapshot()
	if snap == nil {
		return nil, fmt.Errorf("routing configuration not loaded yet")
	}
	rows, healthy := snap.Providers()

	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("admin health: %w", err)
	}
	out := make([]HealthRow, 0, len(rows))
	for _, row := range rows {
		h := HealthRow{Name: row.Name, Enabled: row.Enabled, Healthy: healthy[row.Name]}
		_ = db.QueryRow(ctx, `SELECT MAX(ts) FILTER (WHERE status = 'ok'),
				MAX(ts) FILTER (WHERE status = 'error')
			FROM cost_ledger WHERE provider = $1 AND purpose IS DISTINCT FROM 'test'`, row.Name).Scan(&h.LastSuccess, &h.LastError)
		out = append(out, h)
	}
	return out, nil
}

// TestResult reports one live connection probe.
type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Model     string `json:"model"`
	Detail    string `json:"detail,omitempty"`
}

// Test runs a one-token completion against the provider and books it
// under purpose='test' — visible in the audit trail, invisible to
// usage charts.
func (a *Admin) Test(ctx context.Context, id, model string) (TestResult, error) {
	p, err := a.get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	snap := a.store.Snapshot()
	if snap == nil {
		return TestResult{}, fmt.Errorf("routing configuration not loaded yet")
	}
	drv, ok := snap.Provider(p.Name)
	if !ok {
		return TestResult{}, fmt.Errorf("provider %s not in the serving snapshot; wait for the next reload", p.Name)
	}
	if model == "" {
		model = p.DefaultModel
	}
	if model == "" {
		return TestResult{}, fmt.Errorf("provider %s has no default model; pass one", p.Name)
	}

	res := a.probe(ctx, drv, p.Name, model)
	a.audit(ctx, "test", "provider", id, nil, res)
	return res, nil
}

// Validate runs the same one-token probe as Test against a provider
// config that has NOT been persisted — the UI's validate-on-create.
// The credential_ref must already resolve (the key is stored before
// validation), so a passing validation means the provider is born
// working.
func (a *Admin) Validate(ctx context.Context, p Provider, model string) (TestResult, error) {
	if err := validateProvider(p); err != nil {
		return TestResult{}, err
	}
	if model == "" {
		model = p.DefaultModel
	}
	if model == "" {
		return TestResult{}, fmt.Errorf("a model is required to validate a provider")
	}

	reg, err := provider.Build([]provider.Config{{
		Name:          p.Name,
		Kind:          provider.KindAPI,
		Driver:        p.Driver,
		BaseURL:       p.BaseURL,
		CredentialRef: p.CredentialRef,
		Headers:       p.Headers,
	}}, a.credentialLookup())
	if err != nil {
		return TestResult{}, err
	}
	drv, _ := reg.Get(p.Name)

	res := a.probe(ctx, drv, p.Name, model)
	a.audit(ctx, "validate", "provider", p.Name, nil, res)
	return res, nil
}

// AvailableModels proxies the provider's own model-listing endpoint.
// Drivers that cannot enumerate models (bedrock) return an error the
// UI turns into its manual-entry fallback.
func (a *Admin) AvailableModels(ctx context.Context, id string) ([]provider.AvailableModel, error) {
	p, err := a.get(ctx, id)
	if err != nil {
		return nil, err
	}
	snap := a.store.Snapshot()
	if snap == nil {
		return nil, fmt.Errorf("routing configuration not loaded yet")
	}
	drv, ok := snap.Provider(p.Name)
	if !ok {
		return nil, fmt.Errorf("provider %s not in the serving snapshot; wait for the next reload", p.Name)
	}
	lister, ok := drv.(provider.ModelLister)
	if !ok {
		return nil, fmt.Errorf("driver %s cannot list models: %w", p.Driver, ErrUnsupported)
	}
	lctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	return lister.ListModels(lctx)
}

// credentialLookup mirrors the router store's resolver: a ref that
// fails to resolve builds the driver with an empty key, and the probe
// reports the provider's own auth error instead of a config error.
func (a *Admin) credentialLookup() func(string) string {
	return func(ref string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		val, err := a.secrets.Resolve(ctx, ref)
		if err != nil {
			return ""
		}
		return val
	}
}

// probe runs one one-token completion against drv and books it under
// purpose='test'. Shared by Test (persisted provider) and Validate
// (unsaved config).
func (a *Admin) probe(ctx context.Context, drv provider.Provider, providerName, model string) TestResult {
	tctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	start := time.Now()
	ch, err := drv.Stream(tctx, provider.CompletionRequest{
		Model:     model,
		Messages:  []provider.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	})
	res := TestResult{Model: model}
	entry := ledger.Entry{Provider: providerName, Model: model, TaskCategory: "admin", Purpose: "test"}
	if err != nil {
		res.Detail = err.Error()
		entry.Status, entry.ErrorCode = "error", "invalid_request"
	} else {
		for ev := range ch {
			switch ev.Type {
			case stream.EventError:
				res.Detail = ev.Err.Message
			case stream.EventUsage:
				entry.Usage = ev.Usage
			case stream.EventChunk, stream.EventDone, stream.EventIncomplete:
				res.OK = true
			}
		}
		if res.OK {
			entry.Status = "ok"
		} else {
			entry.Status, entry.ErrorCode = "error", "provider_error"
		}
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	entry.LatencyMS = res.LatencyMS
	a.rec.Record(ctx, entry)
	return res
}

// Sentinel errors the HTTP layer maps onto status codes.
var (
	ErrNotFound    = fmt.Errorf("not found")
	ErrInUse       = fmt.Errorf("in use")
	ErrUnsupported = fmt.Errorf("unsupported")
)

func (a *Admin) get(ctx context.Context, id string) (Provider, error) {
	db, err := a.db.Get()
	if err != nil {
		return Provider{}, fmt.Errorf("admin get: %w", err)
	}
	return scanProvider(ctx, db, id, "")
}

// getForUpdate reads a provider row locked FOR UPDATE within tx: the
// lock is held until the caller commits or rolls back, so a concurrent
// Patch/Delete on the same row blocks instead of racing on a stale read.
func (a *Admin) getForUpdate(ctx context.Context, tx pgx.Tx, id string) (Provider, error) {
	return scanProvider(ctx, tx, id, "FOR UPDATE")
}

func scanProvider(ctx context.Context, q pgxQuerier, id, lock string) (Provider, error) {
	var (
		p            Provider
		models, hdrs []byte
	)
	err := q.QueryRow(ctx, `SELECT id, name, kind, driver, base_url, default_model,
			models, credential_ref, headers, enabled FROM providers WHERE id = $1 `+lock, id).
		Scan(&p.ID, &p.Name, &p.Kind, &p.Driver, &p.BaseURL, &p.DefaultModel,
			&models, &p.CredentialRef, &hdrs, &p.Enabled)
	if err != nil {
		return Provider{}, fmt.Errorf("provider %s: %w", id, ErrNotFound)
	}
	_ = json.Unmarshal(models, &p.Models)
	_ = json.Unmarshal(hdrs, &p.Headers)
	return p, nil
}

// audit records who-did-what; failures log — an audit hiccup must not
// roll back a successful mutation, but it must never be silent.
func (a *Admin) audit(ctx context.Context, action, entity, entityID string, before, after any) {
	db, err := a.db.Get()
	if err != nil {
		a.log.Warn("admin audit skipped", "action", action, "entity", entity, "error", err)
		return
	}
	b, _ := json.Marshal(before)
	aft, _ := json.Marshal(after)
	if _, err := db.Exec(ctx, `INSERT INTO admin_audit (action, entity, entity_id, before, after)
		VALUES ($1, $2, $3, $4, $5)`, action, entity, entityID, b, aft); err != nil {
		a.log.Warn("admin audit failed", "action", action, "entity", entity, "error", err)
	}
}

// reload swaps the serving snapshot; a failure keeps the last good one
// (the poll retries in 30s) and is logged, never returned — the
// mutation itself already committed.
func (a *Admin) reload(ctx context.Context) {
	if err := a.store.Load(ctx); err != nil {
		a.log.Warn("admin reload failed; poll will retry", "error", err)
	}
}

func jsonOr(v any, empty string) []byte {
	b, err := json.Marshal(v)
	if err != nil || v == nil {
		return []byte(empty)
	}
	return b
}
