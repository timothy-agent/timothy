package connectors

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/sandboxclient"
)

// D-162 (issue #1208): an mcp connector may name a command instead of
// an endpoint. Brain never runs it: sandboxd starts it in a service
// container behind mcp-bridge (D-159, D-161), and brain connects to the
// address sandboxd reports exactly as to a remote server.

// bridgeTokenEnv is the env var mcp-bridge reads its bearer token from.
const bridgeTokenEnv = "MCP_BRIDGE_TOKEN" //nolint:gosec // G101: an env var name, not a credential.

// ErrLocalServersUnsupported means the sandbox backend runs no service
// containers (Kubernetes, for now), so command connectors cannot run.
var ErrLocalServersUnsupported = errors.New("local MCP servers need the Docker sandbox backend; this sandbox backend does not run them")

var mcpEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateMCPConfig is the save-time check for kind=mcp: a command
// server's fields and refs. Endpoint-only configs keep today's
// build-time checks.
func validateMCPConfig(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var cfg mcpConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.Command == "" {
		if len(cfg.Args) > 0 || len(cfg.EnvRefs) > 0 || cfg.Internet {
			return errors.New("config.args, config.env_refs and config.internet need config.command")
		}
		return nil
	}
	if cfg.Endpoint != "" {
		return errors.New("config sets both endpoint and command; choose one")
	}
	if cfg.AuthMode == mcpAuthOAuth {
		return errors.New("a command server cannot use oauth auth mode")
	}
	for name, ref := range cfg.EnvRefs {
		if !mcpEnvName.MatchString(name) || name == "PATH" || name == "HOME" || name == bridgeTokenEnv {
			return fmt.Errorf("config.env_refs: %q is not a usable variable name", name)
		}
		if ref == "" || !credentialRefPattern.MatchString(ref) {
			return fmt.Errorf("config.env_refs.%s must be a secret ref name, never a secret value", name)
		}
	}
	return nil
}

// IsCommandMCP reports whether c is an mcp connector that names a
// command.
func IsCommandMCP(c Connector) bool {
	if c.Kind != "mcp" {
		return false
	}
	var cfg mcpConfig
	return json.Unmarshal(c.Config, &cfg) == nil && cfg.Command != ""
}

// MCPServices runs command connectors as sandboxd services.
type MCPServices struct {
	sandbox *sandboxclient.Client
	// key derives each connector's bridge token; tokens are never
	// stored.
	key []byte
	// http reaches services directly: their address comes from
	// sandboxd, never from connector config, so netguard (which
	// refuses private addresses) does not apply.
	http *http.Client
	log  *slog.Logger
	// listenWait bounds how long a new service gets to start listening.
	listenWait time.Duration

	// ensured is the spec key brain last ensured per service, and
	// stoppedSince when a reload first found that service stopped: a
	// reload leaves a crashed, unchanged service stopped for
	// reviveAfter so its error stays visible instead of the crash
	// loop restarting on every reload.
	mu           sync.Mutex
	ensured      map[string]string
	stoppedSince map[string]time.Time
	now          func() time.Time
}

// reviveAfter is how long an automatic reload leaves a crashed service
// stopped; a test, a probe or a config change starts it at once.
const reviveAfter = 10 * time.Minute

// NewMCPServices returns the service runner. key should come from
// secretstore.DeriveKey so tokens stay the same across restarts and an
// unchanged connector keeps its container.
func NewMCPServices(sandbox *sandboxclient.Client, key []byte, log *slog.Logger) *MCPServices {
	return &MCPServices{
		sandbox: sandbox, key: key, http: &http.Client{}, log: log, listenWait: 15 * time.Second,
		ensured: map[string]string{}, stoppedSince: map[string]time.Time{}, now: time.Now,
	}
}

type reviveKey struct{}

// withServiceRevive marks a build the operator asked for (a test): it
// starts a crashed service at once instead of waiting reviveAfter.
func withServiceRevive(ctx context.Context) context.Context {
	return context.WithValue(ctx, reviveKey{}, true)
}

func specKey(spec sandboxclient.ServiceSpec) string {
	raw, _ := json.Marshal(spec)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// holdStopped reports whether a reload should leave service id stopped:
// brain last ensured this same spec and first saw it stopped less than
// reviveAfter ago.
func (s *MCPServices) holdStopped(id, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ensured[id] != key {
		return false
	}
	since, ok := s.stoppedSince[id]
	if !ok {
		s.stoppedSince[id] = s.now()
		return true
	}
	return s.now().Sub(since) < reviveAfter
}

func (s *MCPServices) markEnsured(id, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensured[id] = key
	delete(s.stoppedSince, id)
}

func (s *MCPServices) token(id string) string {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte("mcp-bridge\x00" + id))
	return hex.EncodeToString(mac.Sum(nil))
}

// spec is the service sandboxd runs for cfg: the command behind
// mcp-bridge, env plus the bridge token.
func (s *MCPServices) spec(id string, cfg mcpConfig, env map[string]string) sandboxclient.ServiceSpec {
	full := map[string]string{bridgeTokenEnv: s.token(id)}
	for k, v := range env {
		full[k] = v
	}
	cmd := append([]string{"mcp-bridge", "--", cfg.Command}, cfg.Args...)
	return sandboxclient.ServiceSpec{Command: cmd, Env: full, Internet: cfg.Internet}
}

// resolveEnv resolves cfg's env refs. An error names the variable and
// ref, never a value.
func resolveEnv(ctx context.Context, cfg mcpConfig, resolve Resolve) (map[string]string, error) {
	env := map[string]string{}
	for name, ref := range cfg.EnvRefs {
		if resolve == nil {
			return nil, fmt.Errorf("env %s: no secret store", name)
		}
		v, err := resolve(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("env %s: secret %s does not resolve: %w", name, ref, err)
		}
		env[name] = v
	}
	return env, nil
}

// attach starts (or keeps) the service for connector id and points src
// at it.
func (s *MCPServices) attach(ctx context.Context, id string, src *mcpSource, env map[string]string) error {
	if src.cfg.AuthMode == mcpAuthOAuth {
		return errors.New("a command server cannot use oauth auth mode")
	}
	spec := s.spec(id, src.cfg, env)
	key := specKey(spec)
	if revive, _ := ctx.Value(reviveKey{}).(bool); !revive {
		if st := s.status(ctx, id); st != nil && st.State == "stopped" && s.holdStopped(id, key) {
			return fmt.Errorf("local server %s; it starts again within %s, or on Test", describeService(*st), reviveAfter)
		}
	}
	st, err := s.sandbox.EnsureService(ctx, id, spec)
	if errors.Is(err, sandboxclient.ErrServicesUnsupported) {
		return ErrLocalServersUnsupported
	}
	if err != nil {
		return fmt.Errorf("start local server: %w", err)
	}
	s.markEnsured(id, key)
	if st.State == "stopped" {
		return fmt.Errorf("local server %s", describeService(st))
	}
	if err := s.waitListening(ctx, st.Address); err != nil {
		return err
	}
	src.cfg.Endpoint = st.Address + "/mcp"
	src.cfg.Headers = nil
	src.client = s.http
	src.token = s.token(id)
	src.services = s
	src.serviceID = id
	return nil
}

// waitListening dials the service until its bridge accepts or
// listenWait passes.
func (s *MCPServices) waitListening(ctx context.Context, address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return fmt.Errorf("sandboxd reported an invalid service address")
	}
	ctx, cancel := context.WithTimeout(ctx, s.listenWait)
	defer cancel()
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "tcp", u.Host)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("local server is not listening: %w", err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// status reports service id, or nil when sandboxd has none or cannot
// say.
func (s *MCPServices) status(ctx context.Context, id string) *sandboxclient.ServiceStatus {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	list, err := s.sandbox.ListServices(ctx)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// explain adds service id's state to a failed call, so a crashed
// server reads as one rather than as a connection error.
func (s *MCPServices) explain(ctx context.Context, id string, err error) error {
	st := s.status(ctx, id)
	if st == nil {
		return fmt.Errorf("%w (local server not running)", err)
	}
	if st.State == "running" {
		return err
	}
	return fmt.Errorf("%w (local server %s)", err, describeService(*st))
}

func describeService(st sandboxclient.ServiceStatus) string {
	out := fmt.Sprintf("%s after %d restarts", st.State, st.Restarts)
	if st.LastError != "" {
		out += ": " + st.LastError
	}
	return out
}

// remove stops service id.
func (s *MCPServices) remove(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.sandbox.RemoveService(ctx, id); err != nil {
		s.log.Warn("mcp local server remove failed", "service", id, "error", err)
	}
}

// reconcile removes every service that is not an enabled command
// connector's: disabled, deleted, or left by a probe.
func (s *MCPServices) reconcile(ctx context.Context, keep map[string]bool) {
	list, err := s.sandbox.ListServices(ctx)
	if errors.Is(err, sandboxclient.ErrServicesUnsupported) {
		return
	}
	if err != nil {
		s.log.Warn("mcp local servers not reconciled", "error", err)
		return
	}
	for _, st := range list {
		if !keep[st.ID] {
			s.remove(ctx, st.ID)
		}
	}
}

// supported reports ErrLocalServersUnsupported when the sandbox backend
// runs no services; an unreachable sandboxd is not a refusal.
func (s *MCPServices) supported(ctx context.Context) error {
	if _, err := s.sandbox.ListServices(ctx); errors.Is(err, sandboxclient.ErrServicesUnsupported) {
		return ErrLocalServersUnsupported
	}
	return nil
}

// probe runs the handshake against cfg's command in a throwaway
// service, removed afterwards; env holds plain values that are never
// stored.
func (s *MCPServices) probe(ctx context.Context, cfg mcpConfig, env map[string]string) ProbeResult {
	id := newServiceID()
	defer s.remove(ctx, id)
	src := &mcpSource{name: "probe", cfg: cfg}
	if err := s.attach(withServiceRevive(ctx), id, src, env); err != nil {
		return ProbeResult{Status: ProbeUnreachable, Tools: []ProbeTool{}, Message: err.Error()}
	}
	return probeSource(ctx, s.http, src)
}

// newServiceID is a random uuid v4, the id shape sandboxd accepts.
func newServiceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
