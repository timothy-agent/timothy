package connectors

import "encoding/json"

// SecretRefRole is one secret-store ref a connector row resolves, with
// the role it plays — surfaced by admin/secrets so the ref never looks
// orphaned (or deletable) while a connector still depends on it.
type SecretRefRole struct {
	RefName string
	Role    string
}

// baseCredentialRole reports the role a kind's own CredentialRef plays
// — "oauth_tokens" for a machine-written token bundle (never a valid
// manual pick), "credential" for anything else (a PAT, a bearer token).
// Registered here, next to the kinds whitelist, so a new kind that
// skips this map fails the same way an unregistered builder does —
// silently dormant, not silently wrong (see the consistency test).
var baseCredentialRole = map[string]string{
	"google":    "oauth_tokens", //nolint:gosec // G101: role label, not a credential value.
	"microsoft": "oauth_tokens", //nolint:gosec // G101: role label, not a credential value.
	"github":    "credential",
	"mcp":       "credential",
	"imap":      "credential",
	"caldav":    "credential",
}

// extraSecretRefs maps a kind to the secret refs it resolves beyond its
// own base CredentialRef — an OAuth client secret, a derived signing
// key. A kind with none is simply absent from this map.
var extraSecretRefs = map[string]func(c Connector) []SecretRefRole{
	"google": func(c Connector) []SecretRefRole {
		var cfg GoogleConfig
		if json.Unmarshal(c.Config, &cfg) == nil && cfg.ClientSecretRef != "" {
			return []SecretRefRole{{RefName: cfg.ClientSecretRef, Role: "client_secret"}}
		}
		return nil
	},
	"microsoft": func(c Connector) []SecretRefRole {
		var cfg MicrosoftConfig
		if json.Unmarshal(c.Config, &cfg) == nil && cfg.ClientSecretRef != "" {
			return []SecretRefRole{{RefName: cfg.ClientSecretRef, Role: "client_secret"}}
		}
		return nil
	},
	"github": func(c Connector) []SecretRefRole {
		var cfg GitHubConfig
		if json.Unmarshal(c.Config, &cfg) == nil && (cfg.SignCommits || cfg.SigningPublicKey != "") {
			return []SecretRefRole{{RefName: SigningKeyRefSuffix(c.CredentialRef), Role: "signing_key"}}
		}
		return nil
	},
}

// SecretRefs reports every secret-store ref this connector resolves,
// with its role: the base CredentialRef (role from baseCredentialRole,
// default "credential" for a kind missing from that map — impossible
// for a kind that passed the kinds whitelist, see the consistency
// test), plus any kind-specific extras from extraSecretRefs. Empty
// CredentialRef is included; the caller drops it.
func (c Connector) SecretRefs() []SecretRefRole {
	role, ok := baseCredentialRole[c.Kind]
	if !ok {
		role = "credential"
	}
	refs := []SecretRefRole{{RefName: c.CredentialRef, Role: role}}
	if fn, ok := extraSecretRefs[c.Kind]; ok {
		refs = append(refs, fn(c)...)
	}
	return refs
}
