// Package admin is the gateway's control plane: provider and route
// CRUD over the same tables the router loads (D-004 — providers are
// data), every mutation audited and followed by an in-process snapshot
// reload so changes serve without restarts.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
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

// drivers whitelists what the gateway can actually construct a chat
// driver for — kind='api' rows only. kind='cli' rows (D-051) validate
// against cliDrivers instead: they're mission-only executor providers
// the gateway never builds a chat driver for at all.
var drivers = map[string]bool{"anthropic": true, "openaicompat": true, "openai-responses": true, "bedrock": true}

// cliDrivers whitelists driver names valid on a kind='cli' provider
// row. These never go through provider.Build; only their name and
// wire-format compatibility are validated here (D-051). codex-cli is
// api_key only (no subscription/oauth mode), so in practice it's
// always a kind='api' row — this entry costs nothing to keep either way.
// cursor-cli is subscription-auth only, same posture as claude-cli.
var cliDrivers = map[string]bool{"claude-cli": true, "codex-cli": true, "cursor-cli": true}

// credentialRefPattern accepts names and paths (env var names, Vault
// paths, AWS profile names) and rejects anything that could be a
// pasted secret: no spaces, no long opaque blobs.
var credentialRefPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]{0,128}$`)

// litellmProviderPattern accepts a bare provider token like "xai" or
// "zai_something" — the same shape LiteLLM's own litellm_provider
// values take. It never needs to match anything in the live catalog:
// that catalog syncs and changes independently of this validation.
var litellmProviderPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

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
	catalog *catalog.Store
	log     *slog.Logger
}

func New(db *pgpool.Pool, store *router.Store, rec ledger.Recorder, budgets *ledger.BudgetStore, secrets *secretstore.Store, cat *catalog.Store, log *slog.Logger) *Admin {
	return &Admin{db: db, store: store, rec: rec, budgets: budgets, secrets: secrets, catalog: cat, log: log}
}

// SetSecret pins refName's value to built-in storage regardless of the
// store-wide default backend — for backend-bootstrap credentials (the
// vault token, the ASM static secret key) that can never live behind
// the external backend they unlock. Write-only: the value is never
// read back through any admin endpoint. Reloads the serving snapshot
// so a provider whose credential_ref matches starts resolving
// immediately.
func (a *Admin) SetSecret(ctx context.Context, refName, value string) error {
	if refName == "" {
		return fmt.Errorf("ref name is required")
	}
	if value == "" {
		return fmt.Errorf("value is required")
	}
	if err := a.secrets.SetDB(ctx, refName, value); err != nil {
		return err
	}
	a.audit(ctx, "set", "secret", refName, nil, map[string]bool{"configured": true})
	a.reload(ctx)
	return nil
}

// DeleteSecret removes a stored secret value. Refused while an enabled
// provider still names refName as its credential_ref — the values a
// credential unlocks (chat completions) must never start failing
// silently because its directory entry vanished out from under it.
func (a *Admin) DeleteSecret(ctx context.Context, refName string) error {
	providers, err := a.providersReferencing(ctx, refName)
	if err != nil {
		return err
	}
	if len(providers) > 0 {
		return fmt.Errorf("%s is referenced by provider(s) %v: %w", refName, providers, ErrInUse)
	}
	if err := a.secrets.Delete(ctx, refName); err != nil {
		return err
	}
	a.audit(ctx, "delete", "secret", refName, map[string]bool{"configured": true}, nil)
	a.reload(ctx)
	return nil
}

// providersReferencing returns the names of every provider row whose
// credential_ref matches refName, regardless of enabled state — a
// disabled provider still owns the credential and a delete would strand
// it, so DeleteSecret refuses on any match, not just enabled ones.
func (a *Admin) providersReferencing(ctx context.Context, refName string) ([]string, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("admin secrets: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT name FROM providers WHERE credential_ref = $1 ORDER BY name`, refName)
	if err != nil {
		return nil, fmt.Errorf("admin secrets: referenced by: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("admin secrets: referenced by: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// validSecretBackends is the known backend set, mirroring
// secretstore's own (unexported) validExternalBackend plus "db" — the
// gateway package validates independently since Migrate's target
// backend needs checking before other work (List, per-ref loop), not
// just at the secretstore call site.
var validSecretBackends = map[string]bool{"db": true, "vault": true, "asm": true}

// MigrateSecret moves refName's stored value onto targetBackend,
// wiping its old storage. Audited with backend names only — never the
// value, which never leaves secretstore.Migrate's own transaction of
// external calls.
func (a *Admin) MigrateSecret(ctx context.Context, refName, targetBackend string) error {
	if refName == "" {
		return fmt.Errorf("ref name is required")
	}
	if !validSecretBackends[targetBackend] {
		return fmt.Errorf("unknown backend %q", targetBackend)
	}
	if err := a.secrets.Migrate(ctx, refName, targetBackend); err != nil {
		return err
	}
	a.audit(ctx, "migrate", "secret", refName, nil, map[string]string{"backend": targetBackend})
	a.reload(ctx)
	return nil
}

// SecretMigrationResult is one ref's outcome from a bulk migration:
// exactly one of migrated/skipped is true, or error is set. A partial
// failure never aborts the rest of the batch.
type SecretMigrationResult struct {
	Name     string `json:"name"`
	Migrated bool   `json:"migrated"`
	Skipped  bool   `json:"skipped"`
	Error    string `json:"error,omitempty"`
}

// MigrateAllSecrets moves every stored ref not already on targetBackend
// there, one at a time — a single ref's failure (unreachable backend,
// bad ref name) is recorded and the batch continues, so one bad ref
// never blocks migrating the rest.
func (a *Admin) MigrateAllSecrets(ctx context.Context, targetBackend string) ([]SecretMigrationResult, error) {
	if !validSecretBackends[targetBackend] {
		return nil, fmt.Errorf("unknown backend %q", targetBackend)
	}
	refs, err := a.secrets.List(ctx)
	if err != nil {
		return nil, err
	}
	bootstrap, err := a.secrets.BootstrapRefs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SecretMigrationResult, 0, len(refs))
	for _, r := range refs {
		// A backend's own bootstrap credential can never migrate
		// (Migrate refuses it) — skip it up front instead of reporting
		// the refusal as a batch failure.
		if bootstrap[r.RefName] != "" {
			out = append(out, SecretMigrationResult{Name: r.RefName, Skipped: true})
			continue
		}
		configured, backend, err := a.secrets.Status(ctx, r.RefName)
		if err != nil {
			out = append(out, SecretMigrationResult{Name: r.RefName, Error: err.Error()})
			continue
		}
		if !configured || backend == targetBackend {
			out = append(out, SecretMigrationResult{Name: r.RefName, Skipped: true})
			continue
		}
		if err := a.secrets.Migrate(ctx, r.RefName, targetBackend); err != nil {
			out = append(out, SecretMigrationResult{Name: r.RefName, Error: err.Error()})
			continue
		}
		a.audit(ctx, "migrate", "secret", r.RefName, nil, map[string]string{"backend": targetBackend})
		out = append(out, SecretMigrationResult{Name: r.RefName, Migrated: true})
	}
	a.reload(ctx)
	return out, nil
}

// SecretRef is one stored secret's directory entry: name and
// timestamps, plus the providers that name it as credential_ref. Values
// are never included — ListSecrets exists so the UI can show what
// exists and what would break on delete, nothing more. System marks a
// configured secret backend's own bootstrap credential (see
// secretstore.bootstrapRefs) — Delete refuses these regardless, but the
// UI uses the flag to hide the delete action up front.
type SecretRef struct {
	RefName      string    `json:"ref_name"`
	Backend      string    `json:"backend"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ReferencedBy []string  `json:"referenced_by_providers"`
	System       bool      `json:"system"`
}

// ListSecrets returns every stored secret's directory metadata with the
// providers (by name) that reference it. Connector references are
// brain's own domain (D-057-style split) — the caller (brain's proxy)
// merges those in; this only ever reports what the gateway's own
// tables know about.
func (a *Admin) ListSecrets(ctx context.Context) ([]SecretRef, error) {
	refs, err := a.secrets.List(ctx)
	if err != nil {
		return nil, err
	}
	bootstrap, err := a.secrets.BootstrapRefs(ctx)
	if err != nil {
		return nil, err
	}
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("admin secrets: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT credential_ref, name FROM providers WHERE credential_ref <> '' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("admin secrets: referenced by: %w", err)
	}
	defer rows.Close()
	byRef := map[string][]string{}
	for rows.Next() {
		var ref, name string
		if err := rows.Scan(&ref, &name); err != nil {
			return nil, fmt.Errorf("admin secrets: referenced by: %w", err)
		}
		byRef[ref] = append(byRef[ref], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("admin secrets: referenced by: %w", err)
	}

	out := make([]SecretRef, len(refs))
	for i, r := range refs {
		out[i] = SecretRef{RefName: r.RefName, Backend: r.Backend, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			ReferencedBy: byRef[r.RefName], System: bootstrap[r.RefName] != ""}
	}
	return out, nil
}

// SetSecretValue stores value under refName through the store-wide
// default backend: built-in storage encrypts the value itself, while
// vault/asm write the value into that system.
func (a *Admin) SetSecretValue(ctx context.Context, refName, value string) error {
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

// SecretBackends lists every secret backend with configured/default
// state for the settings UI.
func (a *Admin) SecretBackends(ctx context.Context) ([]secretstore.BackendStatus, error) {
	return a.secrets.Backends(ctx)
}

// SetDefaultSecretBackend moves the single store-wide default that
// SetSecretValue routes through.
func (a *Admin) SetDefaultSecretBackend(ctx context.Context, backend string) error {
	if err := a.secrets.SetDefaultBackend(ctx, backend); err != nil {
		return err
	}
	a.audit(ctx, "set", "secret_backend_default", backend, nil, map[string]string{"default": backend})
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
// scope to its new limit, nil clears it, absent keys stay untouched.
// All keys are validated before any write so a bad entry cannot leave
// a partial update. No snapshot reload: budgets never affect routing.
func (a *Admin) PatchBudget(ctx context.Context, patch map[string]*ledger.BudgetLimit) error {
	for scope, limit := range patch {
		if scope != "day" && scope != "month" {
			return fmt.Errorf("unknown budget scope %q", scope)
		}
		if limit != nil && limit.Amount <= 0 {
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
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	Driver        string            `json:"driver"`
	BaseURL       string            `json:"base_url"`
	DefaultModel  string            `json:"default_model"`
	CredentialRef string            `json:"credential_ref"`
	Headers       map[string]string `json:"headers"`
	Enabled       bool              `json:"enabled"`
	// ExcludeFromBootstrap opts this provider out of auto-fallback fill
	// on the shared default/summarize/embedding routes — for local/dev
	// providers (e.g. Ollama) that should never silently serve
	// production traffic as a fallback.
	ExcludeFromBootstrap bool `json:"exclude_from_bootstrap"`
	// Options is an open bag of driver-specific settings; only
	// reasoning_effort (D-040, openaicompat), request_timeout (D-041, a
	// Go duration string like "20m"), and litellm_provider (an explicit
	// override of the catalog's driver/host-inferred candidate pool,
	// e.g. "xai", "zai") are recognized today.
	Options map[string]string `json:"options"`
}

func validateProvider(p Provider) error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch p.Kind {
	case "api":
		if !drivers[p.Driver] {
			return fmt.Errorf("unknown driver %q", p.Driver)
		}
	case "cli":
		// D-051: kind='cli' rows are mission-only executor providers
		// (e.g. subscription-auth) — no chat driver is ever built for
		// them, so they skip every chat-only check below (credential_ref
		// shape aside). Inherently wire-compatible: the CLI talks to its
		// vendor's own default endpoint under subscription/oauth
		// credentials, never a third-party anthropic-compatible one, so
		// validateHarnessWireFormat (which governs a kind='api' row
		// repurposed as an executor entry) does not apply here.
		if !cliDrivers[p.Driver] {
			return fmt.Errorf("unknown cli driver %q", p.Driver)
		}
	default:
		return fmt.Errorf("kind must be api or cli")
	}
	if !credentialRefPattern.MatchString(p.CredentialRef) {
		return fmt.Errorf("credential_ref must be a name or path (env var, Vault path, AWS profile), never a secret value")
	}
	if _, err := parseRequestTimeout(p.Options); err != nil {
		return err
	}
	if err := validateLitellmProvider(p.Options); err != nil {
		return err
	}
	if err := validateOpenAIResponses(p.Options); err != nil {
		return err
	}
	return nil
}

// harnessDrivers mirrors router.harnessDrivers (unexported there) —
// the set of driver names each known harness accepts directly from a
// kind='api' provider row, independent of the anthropic_base_url
// override. claude-cli speaks anthropic only; pi speaks either
// anthropic or openaicompat (its whole point is dual-wire support);
// codex-cli and opencode speak openaicompat only (codex's own responses
// wire; opencode's config-file baseURL). cursor-cli accepts no api rows
// at all (no BYOK, no custom endpoint support): only its own kind='cli'
// row, which never reaches this check (see the comment below).
var harnessDrivers = map[string]map[string]bool{
	"claude-cli": {"anthropic": true},
	"pi":         {"anthropic": true, "openaicompat": true},
	"codex-cli":  {"openaicompat": true},
	"opencode":   {"openaicompat": true},
	"cursor-cli": {},
}

// validateHarnessWireFormat checks that a kind='api' provider row can
// actually speak a wire protocol harness accepts (D-051, extended for
// pi's dual-wire support), mirroring router.executorUsable's wire
// check exactly so admin can never write a provider the resolve
// endpoint would then mark wire-incompatible: the row's driver must be
// in harnessDrivers[harness], or — only for a harness that accepts the
// anthropic wire at all (codex-cli/opencode never do) — options.
// anthropic_base_url must point at an Anthropic-compatible endpoint.
// Never called for kind='cli' rows — those are inherently
// wire-compatible (D-051, see validateProvider's "cli" case).
// Deliberately does NOT check options.openai_responses (codex-cli's
// stricter requirement, router.harnessNeedsResponses): that flag is
// runtime-probed by Admin.Test, never known at write time, so a
// wire-compatible openaicompat row always validates here regardless of
// whether its endpoint actually serves /responses.
func validateHarnessWireFormat(harness, driver string, opts map[string]string) error {
	accepted, known := harnessDrivers[harness]
	if !known {
		return nil
	}
	if len(accepted) == 0 {
		return fmt.Errorf("harness %q only runs on its own cli provider row", harness)
	}
	overrideOK := accepted["anthropic"] && opts["anthropic_base_url"] != ""
	if !accepted[driver] && !overrideOK {
		return fmt.Errorf("harness %q requires driver in %v or options.anthropic_base_url", harness, sortedDrivers(accepted))
	}
	return nil
}

// sortedDrivers returns m's keys, sorted — keeps the validation error
// message deterministic.
func sortedDrivers(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validateLitellmProvider checks options.litellm_provider, when
// present, is a non-empty printable token. It never validates against
// the live catalog — that syncs and changes independently, so a value
// unrecognized today may be valid tomorrow.
func validateLitellmProvider(opts map[string]string) error {
	raw, ok := opts["litellm_provider"]
	if !ok || raw == "" {
		return nil
	}
	if !litellmProviderPattern.MatchString(raw) {
		return fmt.Errorf("options.litellm_provider %q must be a non-empty printable token", raw)
	}
	return nil
}

// validatePricesByModel rejects an operator-declared price map that
// would corrupt cost records (D-079). Checked here as well as at config
// load so a bad value fails the admin write that introduced it, rather
// than surfacing later as a failed reload far from the operator.
func validatePricesByModel(opts map[string]string) error {
	raw, ok := opts["prices_by_model"]
	if !ok || raw == "" {
		return nil
	}
	var prices map[string]router.ModelPrices
	if err := json.Unmarshal([]byte(raw), &prices); err != nil {
		return fmt.Errorf("options.prices_by_model must be a JSON object of model -> prices: %w", err)
	}
	for model, p := range prices {
		for _, f := range []struct {
			name string
			val  float64
		}{
			{"input_per_mtok", p.InputPerMTok},
			{"output_per_mtok", p.OutputPerMTok},
			{"cache_read_per_mtok", p.CacheReadPerMTok},
			{"cache_write_per_mtok", p.CacheWritePerMTok},
		} {
			if math.IsNaN(f.val) || math.IsInf(f.val, 0) {
				return fmt.Errorf("options.prices_by_model %q: %s is not a finite number", model, f.name)
			}
			if f.val < 0 {
				return fmt.Errorf("options.prices_by_model %q: %s is negative (%v)", model, f.name, f.val)
			}
		}
	}
	return nil
}

// parseRequestTimeout parses options.request_timeout (D-041) into a
// duration. An absent or empty value returns zero — the driver's own
// default applies. An unparseable value is a config error, never a
// silent fallback.
func parseRequestTimeout(opts map[string]string) (time.Duration, error) {
	raw := opts["request_timeout"]
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("options.request_timeout %q: %w", raw, err)
	}
	return d, nil
}

// validateOpenAIResponses checks options.openai_responses, when
// present, is exactly "true" or "false" — the two literals the
// responses probe (Admin.Test) ever writes. Absent means unknown,
// never guessed; an operator should never hand-write anything else here.
func validateOpenAIResponses(opts map[string]string) error {
	raw, ok := opts["openai_responses"]
	if !ok || raw == "" {
		return nil
	}
	if raw != "true" && raw != "false" {
		return fmt.Errorf("options.openai_responses %q must be \"true\" or \"false\"", raw)
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
		credential_ref, headers, options, enabled, exclude_from_bootstrap FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("admin providers: %w", err)
	}
	defer rows.Close()

	out := []Provider{}
	for rows.Next() {
		var (
			p          Provider
			hdrs, opts []byte
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Driver, &p.BaseURL,
			&p.DefaultModel, &p.CredentialRef, &hdrs, &opts, &p.Enabled, &p.ExcludeFromBootstrap); err != nil {
			return nil, fmt.Errorf("admin providers: %w", err)
		}
		if err := json.Unmarshal(hdrs, &p.Headers); err != nil {
			return nil, fmt.Errorf("admin providers: headers: %w", err)
		}
		if err := json.Unmarshal(opts, &p.Options); err != nil {
			return nil, fmt.Errorf("admin providers: options: %w", err)
		}
		normalizeProvider(&p)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Create inserts a provider (disabled by default is the caller's
// choice), audits, auto-seeds default_model when left blank,
// bootstraps the fixed system routes' chains from the catalog's
// candidate models for this provider, and reloads the snapshot.
func (a *Admin) Create(ctx context.Context, p Provider) (string, error) {
	if err := validateProvider(p); err != nil {
		return "", err
	}
	candidates, err := a.catalog.SearchProviders(ctx, "",
		catalog.CandidateProvidersForRow(p.Kind, p.Driver, p.BaseURL, p.Options), 0)
	if err != nil {
		a.log.Warn("provider create: catalog lookup failed", "error", err)
	}
	// kind='cli' rows have no chat driver (D-051) and the UI always
	// sends an explicit default_model for them; auto-seeding from the
	// catalog here would risk a wrong-provider candidate pool leaking
	// a junk model name into a subscription-harness row.
	if p.DefaultModel == "" && p.Kind != "cli" {
		if model, ok := router.CheapestCapable(candidates, "chat"); ok {
			p.DefaultModel = model
		}
	}
	db, err := a.db.Get()
	if err != nil {
		return "", fmt.Errorf("admin create: %w", err)
	}
	hdrs, opts := jsonOr(p.Headers, "{}"), jsonOr(p.Options, "{}")
	var id string
	err = db.QueryRow(ctx, `INSERT INTO providers
			(name, kind, driver, base_url, default_model, credential_ref, headers, options, enabled, exclude_from_bootstrap)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		p.Name, p.Kind, p.Driver, p.BaseURL, p.DefaultModel, p.CredentialRef, hdrs, opts, p.Enabled, p.ExcludeFromBootstrap).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("admin create: %w", err)
	}
	a.audit(ctx, "create", "provider", id, nil, p)
	a.bootstrapRoutes(ctx, router.ProviderRow{ID: id, Kind: p.Kind, ExcludeFromBootstrap: p.ExcludeFromBootstrap}, candidates)
	a.reload(ctx)
	return id, nil
}

// bootstrapRoutes fills the system roles' bound routes' chains from a
// newly connected provider's model metadata (D-033 follow-up, D-049):
// cheapest capable model seeds an empty chain, appends as last
// fallback otherwise, existing order untouched. A role with no route
// yet (fresh install) gets one created, named after the role itself —
// a one-time seed value only; the row's role column is the source of
// truth from then on, and the name is freely renamable. Best-effort —
// a failure here must never fail provider creation; the user can
// always wire routes by hand from the Routing tab.
func (a *Admin) bootstrapRoutes(ctx context.Context, p router.ProviderRow, candidates []catalog.Model) {
	db, err := a.db.Get()
	if err != nil {
		a.log.Warn("route bootstrap skipped", "error", err)
		return
	}
	existing := map[string][]router.ChainEntry{}
	routeName := map[string]string{}
	for _, sr := range router.SystemRoles {
		var (
			name      string
			chainJSON []byte
		)
		err := db.QueryRow(ctx, `SELECT name, chain FROM routes WHERE role = $1`, sr.Role).Scan(&name, &chainJSON)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			name = sr.Role
			if _, err := db.Exec(ctx, `INSERT INTO routes (name, capability, role) VALUES ($1, $2, $1)`,
				name, sr.Capability); err != nil {
				a.log.Warn("route bootstrap: create role route", "role", sr.Role, "error", err)
				continue
			}
			chainJSON = []byte("[]")
		case err != nil:
			a.log.Warn("route bootstrap: read role route", "role", sr.Role, "error", err)
			continue
		}
		var chain []router.ChainEntry
		if err := json.Unmarshal(chainJSON, &chain); err != nil {
			a.log.Warn("route bootstrap: decode chain", "role", sr.Role, "error", err)
			continue
		}
		routeName[sr.Role] = name
		existing[sr.Role] = chain
	}
	updates := router.BootstrapChain(p, existing, candidates)
	for role, chain := range updates {
		name := routeName[role]
		// A route seeded from empty was unusable before (disabled by
		// default, D-033) — bootstrap must flip it on, or the whole
		// point of auto-fill (a freshly connected provider "just
		// works") silently fails with a permanent no_route error.
		// Appending a fallback to an already-configured route leaves
		// enabled untouched: that route's on/off state is an
		// operator decision bootstrap has no business overriding.
		seeded := len(existing[role]) == 0
		var err error
		if seeded {
			_, err = db.Exec(ctx, `UPDATE routes SET chain = $2, enabled = true, updated_at = now() WHERE name = $1`,
				name, jsonOr(chain, "[]"))
		} else {
			_, err = db.Exec(ctx, `UPDATE routes SET chain = $2, updated_at = now() WHERE name = $1`,
				name, jsonOr(chain, "[]"))
		}
		if err != nil {
			a.log.Warn("route bootstrap: write chain", "route", name, "error", err)
			continue
		}
		a.audit(ctx, "bootstrap", "route", name, nil, map[string]any{"chain": chain, "provider": p.ID, "enabled": seeded, "role": role})
	}
}

// Patch applies a partial update. Only fields present in the request
// change; before/after land in the audit row.
type ProviderPatch struct {
	BaseURL              *string            `json:"base_url"`
	DefaultModel         *string            `json:"default_model"`
	CredentialRef        *string            `json:"credential_ref"`
	Headers              *map[string]string `json:"headers"`
	Enabled              *bool              `json:"enabled"`
	ExcludeFromBootstrap *bool              `json:"exclude_from_bootstrap"`
	Options              *map[string]string `json:"options"`
}

func (a *Admin) Patch(ctx context.Context, id string, patch ProviderPatch) error {
	if patch.CredentialRef != nil && !credentialRefPattern.MatchString(*patch.CredentialRef) {
		return fmt.Errorf("credential_ref must be a name or path, never a secret value")
	}
	if patch.Options != nil {
		if _, err := parseRequestTimeout(*patch.Options); err != nil {
			return err
		}
		if err := validateLitellmProvider(*patch.Options); err != nil {
			return err
		}
		if err := validatePricesByModel(*patch.Options); err != nil {
			return err
		}
		if err := validateOpenAIResponses(*patch.Options); err != nil {
			return err
		}
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
	if patch.CredentialRef != nil {
		after.CredentialRef = *patch.CredentialRef
	}
	if patch.Headers != nil {
		after.Headers = *patch.Headers
	}
	if patch.Enabled != nil {
		after.Enabled = *patch.Enabled
	}
	if patch.ExcludeFromBootstrap != nil {
		after.ExcludeFromBootstrap = *patch.ExcludeFromBootstrap
	}
	if patch.Options != nil {
		after.Options = *patch.Options
	}

	tag, err := tx.Exec(ctx, `UPDATE providers SET base_url = $2, default_model = $3,
			credential_ref = $4, headers = $5, enabled = $6, exclude_from_bootstrap = $7, options = $8, updated_at = now()
		WHERE id = $1`,
		id, after.BaseURL, after.DefaultModel,
		after.CredentialRef, jsonOr(after.Headers, "{}"), after.Enabled, after.ExcludeFromBootstrap, jsonOr(after.Options, "{}"))
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

	// routes isn't locked here, so a concurrent PatchRoute could
	// still commit a fresh reference right after this check reads zero.
	// PatchRoute's own provider-existence check runs inside its own
	// FOR UPDATE-guarded tx against this same row, so the two race
	// safely: whichever commits first wins, the other sees the updated
	// state and fails cleanly (delete finds a ref, or the route patch
	// finds the provider gone).
	var refs int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM routes
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

// Route is the API shape of one routes row. Chain/Strategy/Enabled are
// DB truth (what PATCH edits); Resolved and Serving are derived from
// the live snapshot — the router's actual try order with the stats and
// scores behind it. Both are empty for disabled routes (the snapshot
// holds enabled routes only) or when no snapshot is loaded yet.
type Route struct {
	Name       string              `json:"name"`
	Chain      []router.ChainEntry `json:"chain"`
	Strategy   string              `json:"strategy"`
	Enabled    bool                `json:"enabled"`
	Capability string              `json:"capability"`
	Role       string              `json:"role,omitempty"`
	Resolved   []RouteEntryStatus  `json:"resolved,omitempty"`
	Serving    *router.ChainEntry  `json:"serving,omitempty"`
}

// RouteEntryStatus is one resolved chain entry with the router's gate
// verdict and scoring factors. Nullable numerics are nil when the
// ledger has no data (or the model is unpriced) — never a guessed 0.
type RouteEntryStatus struct {
	ProviderID    string   `json:"provider_id"`
	ProviderName  string   `json:"provider_name,omitempty"`
	ProviderKind  string   `json:"provider_kind,omitempty"`
	Model         string   `json:"model"`
	Usable        bool     `json:"usable"`
	SkipReason    string   `json:"skip_reason,omitempty"`
	Score         *float64 `json:"score,omitempty"`
	NormPrice     *float64 `json:"norm_price,omitempty"`
	NormLatency   *float64 `json:"norm_latency,omitempty"`
	NormTPS       *float64 `json:"norm_tps,omitempty"`
	Uptime        *float64 `json:"uptime,omitempty"`
	LatencyMS     *float64 `json:"latency_ms,omitempty"`
	TokensPerS    *float64 `json:"tokens_per_s,omitempty"`
	InputPerMTok  *float64 `json:"input_per_mtok,omitempty"`
	OutputPerMTok *float64 `json:"output_per_mtok,omitempty"`
}

// resolvedForRoute maps the router's annotated try order to wire
// shape and picks the first usable entry as the serving one.
func resolvedForRoute(snap *router.Snapshot, name string) ([]RouteEntryStatus, *router.ChainEntry) {
	detail := snap.ResolveDetail(name)
	if len(detail) == 0 {
		return nil, nil
	}
	opt := func(v, none float64) *float64 {
		if v == none {
			return nil
		}
		return &v
	}
	out := make([]RouteEntryStatus, len(detail))
	var serving *router.ChainEntry
	for i, d := range detail {
		out[i] = RouteEntryStatus{
			ProviderID:    d.Entry.ProviderID,
			ProviderName:  d.ProviderName,
			ProviderKind:  d.ProviderKind,
			Model:         d.Model,
			Usable:        d.Usable,
			SkipReason:    d.SkipReason,
			Uptime:        opt(d.Uptime, -1),
			LatencyMS:     opt(d.LatencyMS, 0),
			TokensPerS:    opt(d.TokensPerS, 0),
			InputPerMTok:  opt(d.InputPerMTok, 0),
			OutputPerMTok: opt(d.OutputPerMTok, 0),
		}
		if d.Scored {
			out[i].Score = &detail[i].Score
			out[i].NormPrice = opt(d.NormPrice, -1)
			out[i].NormLatency = opt(d.NormLatency, -1)
			out[i].NormTPS = opt(d.NormTPS, -1)
		}
		if serving == nil && d.Usable {
			entry := d.Entry
			serving = &entry
		}
	}
	return out, serving
}

func (a *Admin) Routes(ctx context.Context) ([]Route, error) {
	db, err := a.db.Get()
	if err != nil {
		return nil, fmt.Errorf("admin routes: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT name, chain, strategy, enabled, capability, role FROM routes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("admin routes: %w", err)
	}
	defer rows.Close()

	out := []Route{}
	for rows.Next() {
		var (
			r     Route
			chain []byte
			role  *string
		)
		if err := rows.Scan(&r.Name, &chain, &r.Strategy, &r.Enabled, &r.Capability, &role); err != nil {
			return nil, fmt.Errorf("admin routes: %w", err)
		}
		if err := json.Unmarshal(chain, &r.Chain); err != nil {
			return nil, fmt.Errorf("admin routes: chain: %w", err)
		}
		if role != nil {
			r.Role = *role
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if snap := a.store.Snapshot(); snap != nil {
		for i := range out {
			if out[i].Enabled {
				out[i].Resolved, out[i].Serving = resolvedForRoute(snap, out[i].Name)
			}
		}
	}
	return out, nil
}

// RoutePatch reorders/replaces a route's chain, changes its strategy,
// and/or flips it. Chain is raw JSON, not []router.ChainEntry directly:
// PatchRoute decodes it itself so it can also detect (and reject) a
// legacy "harness" key on an entry — harness selection moved to the
// mission column (D-051), and json.Unmarshal would otherwise silently
// drop that key into a plain {provider_id, model} entry.
type RoutePatch struct {
	Chain    *json.RawMessage `json:"chain"`
	Strategy *string          `json:"strategy"`
	Enabled  *bool            `json:"enabled"`
}

var validStrategies = map[string]bool{"ordered": true, "auto": true, "price": true, "latency": true}

func (a *Admin) PatchRoute(ctx context.Context, name string, patch RoutePatch) error {
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
	err = tx.QueryRow(ctx, `SELECT name, chain, strategy, enabled FROM routes WHERE name = $1`,
		name).Scan(&before.Name, &chainJSON, &before.Strategy, &before.Enabled)
	if err != nil {
		return fmt.Errorf("route %s: %w", name, ErrNotFound)
	}
	_ = json.Unmarshal(chainJSON, &before.Chain)

	after := before
	if patch.Chain != nil {
		// harnessProbe catches a legacy or mistaken "harness" key on any
		// entry: harness selection moved to the mission column (D-051),
		// so a chain entry is pure {provider_id, model} again — reject
		// the write explicitly rather than silently dropping the key
		// (router.ChainEntry has no Harness field to decode it into).
		var harnessProbe []struct {
			Harness string `json:"harness"`
		}
		if err := json.Unmarshal(*patch.Chain, &harnessProbe); err != nil {
			return fmt.Errorf("chain: %w", err)
		}
		for _, e := range harnessProbe {
			if e.Harness != "" {
				return fmt.Errorf("chain entry harness %q rejected: harness moved to mission, not the route chain", e.Harness)
			}
		}
		var chain []router.ChainEntry
		if err := json.Unmarshal(*patch.Chain, &chain); err != nil {
			return fmt.Errorf("chain: %w", err)
		}
		// Every entry must reference an existing provider — a typo'd id
		// becomes a silent skip at resolve time otherwise. FOR UPDATE
		// locks the referenced provider row against a concurrent Delete
		// racing to remove it after this check passes.
		for _, e := range chain {
			if err := tx.QueryRow(ctx, `SELECT 1 FROM providers WHERE id = $1 FOR UPDATE`, e.ProviderID).Scan(new(int)); err != nil {
				return fmt.Errorf("chain entry references unknown provider %s", e.ProviderID)
			}
		}
		after.Chain = chain
	}
	if patch.Strategy != nil {
		if !validStrategies[*patch.Strategy] {
			return fmt.Errorf("unknown strategy %q", *patch.Strategy)
		}
		after.Strategy = *patch.Strategy
	}
	if patch.Enabled != nil {
		after.Enabled = *patch.Enabled
	}

	if _, err := tx.Exec(ctx, `UPDATE routes SET chain = $2, strategy = $3, enabled = $4, updated_at = now()
		WHERE name = $1`, name, jsonOr(after.Chain, "[]"), after.Strategy, after.Enabled); err != nil {
		return fmt.Errorf("admin route patch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin route patch: %w", err)
	}
	a.audit(ctx, "update", "route", name, before, after)
	a.reload(ctx)
	return nil
}

var validCapabilities = map[string]bool{"chat": true, "embeddings": true, "vision": true}

// CreateRoute makes a new, plain user-owned route: no role, empty
// chain, disabled until chained (D-049 — routing has no hardcoded
// names; any route beyond the 4 required system roles is entirely
// user-managed).
func (a *Admin) CreateRoute(ctx context.Context, name, capability string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("route name is required")
	}
	if !validCapabilities[capability] {
		return "", fmt.Errorf("unknown capability %q", capability)
	}
	db, err := a.db.Get()
	if err != nil {
		return "", fmt.Errorf("admin create route: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO routes (name, capability) VALUES ($1, $2) RETURNING id`,
		name, capability).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("admin create route: %w", err)
	}
	a.audit(ctx, "create", "route", name, nil, map[string]any{"capability": capability})
	a.reload(ctx)
	return id, nil
}

// DeleteRoute removes a route unless it's bound to one of the 4
// required system roles — Timothy would stop working the moment its
// only chat, embedding, vision, or summarize route disappeared, so
// that must be an explicit role reassignment first, never an
// incidental delete.
func (a *Admin) DeleteRoute(ctx context.Context, name string) error {
	db, err := a.db.Get()
	if err != nil {
		return fmt.Errorf("admin delete route: %w", err)
	}
	var role *string
	err = db.QueryRow(ctx, `SELECT role FROM routes WHERE name = $1`, name).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("route %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("admin delete route: %w", err)
	}
	if role != nil {
		return fmt.Errorf("route %s serves required role %q: %w", name, *role, ErrInUse)
	}
	if _, err := db.Exec(ctx, `DELETE FROM routes WHERE name = $1`, name); err != nil {
		return fmt.Errorf("admin delete route: %w", err)
	}
	a.audit(ctx, "delete", "route", name, nil, nil)
	a.reload(ctx)
	return nil
}

var validRoles = map[string]bool{"default": true, "embedding": true, "vision": true, "summarize": true}

// SetRouteRole moves role to be served by the route named name,
// unbinding whichever route currently holds it (if any) in the same
// transaction — the partial unique index on routes.role would reject
// a bad double-write anyway, but doing it explicitly keeps the audit
// trail clean.
func (a *Admin) SetRouteRole(ctx context.Context, name, role string) error {
	if !validRoles[role] {
		return fmt.Errorf("unknown role %q", role)
	}
	db, err := a.db.Get()
	if err != nil {
		return fmt.Errorf("admin set route role: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("admin set route role: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var target string
	if err := tx.QueryRow(ctx, `SELECT name FROM routes WHERE name = $1 FOR UPDATE`, name).Scan(&target); err != nil {
		return fmt.Errorf("route %s: %w", name, ErrNotFound)
	}
	if _, err := tx.Exec(ctx, `UPDATE routes SET role = NULL, updated_at = now() WHERE role = $1 AND name != $2`, role, name); err != nil {
		return fmt.Errorf("admin set route role: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE routes SET role = $2, updated_at = now() WHERE name = $1`, name, role); err != nil {
		return fmt.Errorf("admin set route role: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin set route role: %w", err)
	}
	a.audit(ctx, "update", "route_role", name, nil, map[string]any{"role": role})
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
	// ResponsesOK is whether the endpoint serves POST /responses (the
	// OpenAI Responses API codex-cli requires) — nil when unprobed or
	// the probe outcome was ambiguous (never guessed), set only for an
	// openaicompat provider with a base_url. Never affects OK: a
	// provider without /responses is still a perfectly good chat
	// provider for every harness/route that doesn't need it.
	ResponsesOK *bool `json:"responses_ok,omitempty"`
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

	timeout := probeTimeout(p.Options)
	res := a.probe(ctx, drv, p.Name, model, snap.Prices(p.Name, model), timeout)
	if res.OK && (p.Driver == "openaicompat" || p.Driver == "openai-responses") && p.BaseURL != "" {
		res.ResponsesOK = a.probeResponses(ctx, p.BaseURL, p.CredentialRef, model, timeout)
		if res.ResponsesOK != nil {
			if err := a.setOpenAIResponses(ctx, id, *res.ResponsesOK); err != nil {
				a.log.Warn("persist openai_responses probe result failed", "provider", id, "error", err)
			}
		}
	}
	a.audit(ctx, "test", "provider", id, nil, res)
	return res, nil
}

// setOpenAIResponses merges options.openai_responses into the provider
// row's existing options (never clobbering other keys) and reloads the
// serving snapshot, mirroring Patch's own read-modify-write-then-reload
// shape but scoped to this one key.
func (a *Admin) setOpenAIResponses(ctx context.Context, id string, ok bool) error {
	db, err := a.db.Get()
	if err != nil {
		return fmt.Errorf("admin set openai_responses: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("admin set openai_responses: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := a.getForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	opts := map[string]string{}
	for k, v := range before.Options {
		opts[k] = v
	}
	opts["openai_responses"] = strconv.FormatBool(ok)

	if _, err := tx.Exec(ctx, `UPDATE providers SET options = $2, updated_at = now() WHERE id = $1`,
		id, jsonOr(opts, "{}")); err != nil {
		return fmt.Errorf("admin set openai_responses: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("admin set openai_responses: %w", err)
	}
	a.reload(ctx)
	return nil
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

	timeout, err := parseRequestTimeout(p.Options)
	if err != nil {
		return TestResult{}, err
	}
	reg, err := provider.Build([]provider.Config{{
		Name:            p.Name,
		Kind:            provider.KindAPI,
		Driver:          p.Driver,
		BaseURL:         p.BaseURL,
		CredentialRef:   p.CredentialRef,
		Headers:         p.Headers,
		ReasoningEffort: p.Options["reasoning_effort"],
		Region:          p.Options["region"],
		Timeout:         timeout,
	}}, a.credentialLookup())
	if err != nil {
		return TestResult{}, err
	}
	drv, _ := reg.Get(p.Name)

	probeTO := probeTimeout(p.Options)
	res := a.probe(ctx, drv, p.Name, model, nil, probeTO)
	if res.OK && (p.Driver == "openaicompat" || p.Driver == "openai-responses") && p.BaseURL != "" {
		// Report-only: Validate probes an unsaved config, so there is
		// nothing to persist the result onto.
		res.ResponsesOK = a.probeResponses(ctx, p.BaseURL, p.CredentialRef, model, probeTO)
	}
	a.audit(ctx, "validate", "provider", p.Name, nil, res)
	return res, nil
}

// AvailableModels proxies the provider's own model-listing endpoint.
// Drivers that cannot enumerate models (bedrock) return an error the
// UI turns into its manual-entry fallback. kind='cli' rows never build
// a chat driver (D-051, BuildSnapshot skips them entirely) so they
// never reach the serving snapshot below; cursor-cli is the one cli
// driver with a listing endpoint of its own, called directly.
func (a *Admin) AvailableModels(ctx context.Context, id string) ([]provider.AvailableModel, error) {
	p, err := a.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Kind == "cli" {
		if p.Driver != "cursor-cli" {
			return nil, fmt.Errorf("driver %s cannot list models: %w", p.Driver, ErrUnsupported)
		}
		return a.cursorAvailableModels(ctx, p)
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

// probeTimeout bounds a connection probe's context: a provider with
// options.request_timeout set (D-041) — a slow CPU-only remote Ollama,
// say — gets that value instead of the fixed 20s default, so its own
// probe isn't killed by a ceiling shorter than the timeout it declared
// for real traffic. request_timeout was already validated when the
// provider was written, so a parse failure here can only mean stale
// data racing a concurrent edit; falling back to the default is safer
// than failing the probe outright.
func probeTimeout(opts map[string]string) time.Duration {
	if d, err := parseRequestTimeout(opts); err == nil && d > 0 {
		return d
	}
	return testTimeout
}

// probe runs one one-token completion against drv and books it under
// purpose='test'. Shared by Test (persisted provider) and Validate
// (unsaved config). timeout bounds the probe's context — testTimeout
// (20s) unless the provider declares its own options.request_timeout.
func (a *Admin) probe(ctx context.Context, drv provider.Provider, providerName, model string, prices *router.ModelPrices, timeout time.Duration) TestResult {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	ch, err := drv.Stream(tctx, provider.CompletionRequest{
		Model:     model,
		Messages:  []provider.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	})
	res := TestResult{Model: model}
	entry := ledger.Entry{Provider: providerName, Model: model, Route: "admin", Purpose: "test"}
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
			entry.Cost = ledger.Cost(prices, entry.Usage)
			if prices != nil {
				entry.Currency = prices.Currency
			}
		} else {
			entry.Status, entry.ErrorCode = "error", "provider_error"
		}
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	entry.LatencyMS = res.LatencyMS
	a.rec.Record(ctx, entry)
	return res
}

// responsesProbeBody is the minimal OpenAI Responses API request the
// capability probe sends: max_output_tokens' floor is 16 (the API
// rejects anything lower), so this is the smallest legal request that
// still exercises the endpoint.
type responsesProbeBody struct {
	Model           string `json:"model"`
	Input           string `json:"input"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

// probeResponses checks whether baseURL serves POST /responses — the
// OpenAI Responses API wire codex-cli requires (D-051 follow-up: the
// Z.ai coding-plan endpoint 404s /responses despite chatting fine over
// /chat/completions, so the static openaicompat wire check alone can't
// tell missions codex-cli will actually work there). Only ever called
// after the chat probe already succeeded, and never affects
// TestResult.OK — a provider without /responses is still a perfectly
// good chat provider. Result is deliberately tri-state: 2xx is a
// confirmed true, 404/405 (route not found/not allowed — the
// unambiguous "this endpoint doesn't have /responses" signals) is a
// confirmed false, anything else (network error, timeout, 401/403/429,
// 5xx) is nil — an ambiguous signal must never be recorded as a
// definite no. No ledger entry: this is capability detection, not
// spend, and cost is unknown either way (never guessed) even if the
// response happens to carry a usage field.
func (a *Admin) probeResponses(ctx context.Context, baseURL, credentialRef, model string, timeout time.Duration) *bool {
	body, err := json.Marshal(responsesProbeBody{Model: model, Input: "ping", MaxOutputTokens: 16})
	if err != nil {
		return nil
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if credentialRef != "" {
		req.Header.Set("Authorization", "Bearer "+a.credentialLookup()(credentialRef))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		v := true
		return &v
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		v := false
		return &v
	default:
		return nil
	}
}

// Sentinel errors the HTTP layer maps onto status codes.
var (
	ErrNotFound    = fmt.Errorf("not found")
	ErrInUse       = fmt.Errorf("in use")
	ErrUnsupported = fmt.Errorf("unsupported")
	// ErrUpstream marks a failure reaching a third-party API the gateway
	// itself calls out to (e.g. Cursor's model listing): a 502, not a
	// 400, since the request was fine but the upstream wasn't reachable.
	ErrUpstream = fmt.Errorf("upstream unavailable")
)

func (a *Admin) get(ctx context.Context, id string) (Provider, error) {
	db, err := a.db.Get()
	if err != nil {
		return Provider{}, fmt.Errorf("admin get: %w", err)
	}
	return scanProvider(ctx, db, id, "")
}

// getByName looks up a provider row by its name column — cost_ledger
// rows carry the provider's name, not its id, so CatalogPrices resolves
// a ledger row's provider this way rather than by id.
func (a *Admin) getByName(ctx context.Context, name string) (Provider, error) {
	db, err := a.db.Get()
	if err != nil {
		return Provider{}, fmt.Errorf("admin get by name: %w", err)
	}
	return scanProviderByName(ctx, db, name)
}

// getForUpdate reads a provider row locked FOR UPDATE within tx: the
// lock is held until the caller commits or rolls back, so a concurrent
// Patch/Delete on the same row blocks instead of racing on a stale read.
func (a *Admin) getForUpdate(ctx context.Context, tx pgx.Tx, id string) (Provider, error) {
	return scanProvider(ctx, tx, id, "FOR UPDATE")
}

func scanProvider(ctx context.Context, q pgxQuerier, id, lock string) (Provider, error) {
	var (
		p          Provider
		hdrs, opts []byte
	)
	err := q.QueryRow(ctx, `SELECT id, name, kind, driver, base_url, default_model,
			credential_ref, headers, options, enabled, exclude_from_bootstrap FROM providers WHERE id = $1 `+lock, id).
		Scan(&p.ID, &p.Name, &p.Kind, &p.Driver, &p.BaseURL, &p.DefaultModel,
			&p.CredentialRef, &hdrs, &opts, &p.Enabled, &p.ExcludeFromBootstrap)
	if err != nil {
		return Provider{}, fmt.Errorf("provider %s: %w", id, ErrNotFound)
	}
	_ = json.Unmarshal(hdrs, &p.Headers)
	_ = json.Unmarshal(opts, &p.Options)
	normalizeProvider(&p)
	return p, nil
}

func scanProviderByName(ctx context.Context, q pgxQuerier, name string) (Provider, error) {
	var (
		p          Provider
		hdrs, opts []byte
	)
	err := q.QueryRow(ctx, `SELECT id, name, kind, driver, base_url, default_model,
			credential_ref, headers, options, enabled, exclude_from_bootstrap FROM providers WHERE name = $1`, name).
		Scan(&p.ID, &p.Name, &p.Kind, &p.Driver, &p.BaseURL, &p.DefaultModel,
			&p.CredentialRef, &hdrs, &opts, &p.Enabled, &p.ExcludeFromBootstrap)
	if err != nil {
		return Provider{}, fmt.Errorf("provider %s: %w", name, ErrNotFound)
	}
	_ = json.Unmarshal(hdrs, &p.Headers)
	_ = json.Unmarshal(opts, &p.Options)
	normalizeProvider(&p)
	return p, nil
}

// normalizeProvider papers over jsonb null in rows written before
// jsonOr guarded against typed nils: clients get [] / {}, never null.
func normalizeProvider(p *Provider) {
	if p.Headers == nil {
		p.Headers = map[string]string{}
	}
	if p.Options == nil {
		p.Options = map[string]string{}
	}
}

// audit records who-did-what; failures log — an audit hiccup must not
// roll back a successful mutation, but it must never be silent.
func (a *Admin) audit(ctx context.Context, action, entity, entityID string, before, after any) {
	db, err := a.db.Get()
	if err != nil {
		a.log.Warn("admin audit skipped", "action", action, "entity", entity, "error", err)
		return
	}
	b, _ := json.Marshal(redactAuditValue(before))
	aft, _ := json.Marshal(redactAuditValue(after))
	if _, err := db.Exec(ctx, `INSERT INTO admin_audit (action, entity, entity_id, before, after)
		VALUES ($1, $2, $3, $4, $5)`, action, entity, entityID, b, aft); err != nil {
		a.log.Warn("admin audit failed", "action", action, "entity", entity, "error", err)
	}
}

// redactAuditValue blanks Provider.Headers values before a payload is
// written to admin_audit: header-authenticated providers put
// Authorization/x-api-key values there, and admin_audit is a plain
// table with no dedicated secret handling. Copies the map so the live
// struct (and whatever the caller does with it afterward) is untouched.
func redactAuditValue(v any) any {
	p, ok := v.(Provider)
	if !ok {
		return v
	}
	if len(p.Headers) == 0 {
		return p
	}
	redacted := make(map[string]string, len(p.Headers))
	for k := range p.Headers {
		redacted[k] = "[redacted]"
	}
	p.Headers = redacted
	return p
}

// reload swaps the serving snapshot; a failure keeps the last good one
// (the poll retries in 30s) and is logged, never returned — the
// mutation itself already committed.
func (a *Admin) reload(ctx context.Context) {
	if err := a.store.Load(ctx); err != nil {
		a.log.Warn("admin reload failed; poll will retry", "error", err)
	}
}

// jsonOr never returns "null": a nil slice or map inside v marshals to
// JSON null (v == nil is false for typed nils), which would land in a
// jsonb column and come back as null to API clients that expect [] / {}.
func jsonOr(v any, empty string) []byte {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(empty)
	}
	return b
}
