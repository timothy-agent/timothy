package connectors

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestBaseCredentialRoleRegisteredForEveryKind guards the exact gap
// that let "microsoft" ship mislabeled: every whitelisted kind must
// declare its base CredentialRef's role explicitly, so a new kind added
// to `kinds` without one fails a test instead of silently defaulting.
func TestBaseCredentialRoleRegisteredForEveryKind(t *testing.T) {
	for kind := range kinds {
		if _, ok := baseCredentialRole[kind]; !ok {
			t.Errorf("kind %q has no baseCredentialRole entry", kind)
		}
	}
}

func TestConnectorSecretRefsGoogleClientSecret(t *testing.T) {
	//nolint:gosec // G101: fixture ref names, not credential values.
	c := Connector{
		Kind: "google", CredentialRef: "GMAIL_GOOGLE_OAUTH",
		Config: []byte(`{"client_id":"x","client_secret_ref":"GMAIL_GOOGLE_CLIENT_SECRET"}`),
	}
	got := c.SecretRefs()
	want := []SecretRefRole{
		{RefName: "GMAIL_GOOGLE_OAUTH", Role: "oauth_tokens"},
		{RefName: "GMAIL_GOOGLE_CLIENT_SECRET", Role: "client_secret"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SecretRefs() = %+v, want %+v", got, want)
	}
}

func TestConnectorSecretRefsMicrosoftClientSecret(t *testing.T) {
	//nolint:gosec // G101: fixture ref names, not credential values.
	c := Connector{
		Kind: "microsoft", CredentialRef: "OUTLOOK_MICROSOFT_OAUTH",
		Config: []byte(`{"client_id":"x","client_secret_ref":"OUTLOOK_MICROSOFT_CLIENT_SECRET"}`),
	}
	got := c.SecretRefs()
	want := []SecretRefRole{
		{RefName: "OUTLOOK_MICROSOFT_OAUTH", Role: "oauth_tokens"},
		{RefName: "OUTLOOK_MICROSOFT_CLIENT_SECRET", Role: "client_secret"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SecretRefs() = %+v, want %+v", got, want)
	}
}

// TestExtraSecretRefsSigningKeyForEveryGitKind guards the gap that let
// a bitbucket signing key look orphaned to admin/secrets: every git
// kind must resolve its derived signing-key ref, so a new one added to
// GitKinds without an extraSecretRefs entry fails a test.
func TestExtraSecretRefsSigningKeyForEveryGitKind(t *testing.T) {
	for kind := range GitKinds {
		c := Connector{Kind: kind, CredentialRef: "MYCONN_TOKEN", Config: []byte(`{"sign_commits":true}`)}
		var found bool
		for _, ref := range c.SecretRefs() {
			if ref.Role == "signing_key" && ref.RefName == SigningKeyRefSuffix("MYCONN_TOKEN") {
				found = true
			}
		}
		if !found {
			t.Errorf("git kind %q resolves no signing_key secret ref", kind)
		}
	}
}

func TestConnectorSecretRefsSigningKey(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		config string
		want   []SecretRefRole
	}{
		{
			name: "github signing on", kind: "github", config: `{"sign_commits":true}`,
			want: []SecretRefRole{
				{RefName: "MYCONN_PAT", Role: "credential"},
				{RefName: SigningKeyRefSuffix("MYCONN_PAT"), Role: "signing_key"},
			},
		},
		{
			name: "bitbucket signing on beside its workspace", kind: "bitbucket", config: `{"workspace":"acme","sign_commits":true}`,
			want: []SecretRefRole{
				{RefName: "MYCONN_PAT", Role: "credential"},
				{RefName: SigningKeyRefSuffix("MYCONN_PAT"), Role: "signing_key"},
			},
		},
		{
			name: "bitbucket public key without the flag still holds the ref", kind: "bitbucket", config: `{"signing_public_key":"ssh-ed25519 AAAA"}`,
			want: []SecretRefRole{
				{RefName: "MYCONN_PAT", Role: "credential"},
				{RefName: SigningKeyRefSuffix("MYCONN_PAT"), Role: "signing_key"},
			},
		},
		{
			name: "bitbucket signing off", kind: "bitbucket", config: `{"workspace":"acme"}`,
			want: []SecretRefRole{{RefName: "MYCONN_PAT", Role: "credential"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Connector{Kind: tt.kind, CredentialRef: "MYCONN_PAT", Config: []byte(tt.config)}
			got := c.SecretRefs()
			if len(got) != len(tt.want) {
				t.Fatalf("SecretRefs() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("SecretRefs() = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

// TestConnectorSecretRefsSSHKey covers the transport key's own role
// (issue #796): listed once ssh_transport is on or a public key was
// ever written back, so admin/secrets never reports it orphaned.
func TestConnectorSecretRefsSSHKey(t *testing.T) {
	for _, tt := range []struct {
		name      string
		config    string
		wantRoles []string
	}{
		{"neither feature on", `{}`, nil},
		{"ssh transport on", `{"ssh_transport":true}`, []string{"ssh_key"}},
		{"public key written back", `{"ssh_public_key":"ssh-ed25519 AAAA"}`, []string{"ssh_key"}},
		{"both features on", `{"sign_commits":true,"ssh_transport":true}`, []string{"signing_key", "ssh_key"}},
		{"signing only", `{"sign_commits":true}`, []string{"signing_key"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Connector{Kind: "github", CredentialRef: "MYCONN_PAT", Config: json.RawMessage(tt.config)}
			var got []string
			for _, ref := range c.SecretRefs() {
				if ref.Role == "credential" {
					continue
				}
				got = append(got, ref.Role)
				switch ref.Role {
				case "signing_key":
					if ref.RefName != SigningKeyRefSuffix("MYCONN_PAT") {
						t.Fatalf("signing ref = %q", ref.RefName)
					}
				case "ssh_key":
					if ref.RefName != SSHKeyRefSuffix("MYCONN_PAT") {
						t.Fatalf("ssh ref = %q", ref.RefName)
					}
				}
			}
			if !reflect.DeepEqual(got, tt.wantRoles) {
				t.Fatalf("roles = %v, want %v", got, tt.wantRoles)
			}
		})
	}
}

// Every git kind must resolve the transport ref, the same gap guard
// TestExtraSecretRefsSigningKeyForEveryGitKind applies to signing.
func TestExtraSecretRefsSSHKeyForEveryGitKind(t *testing.T) {
	for kind := range GitKinds {
		c := Connector{Kind: kind, CredentialRef: "MYCONN_TOKEN", Config: json.RawMessage(`{"ssh_transport":true}`)}
		found := false
		for _, ref := range c.SecretRefs() {
			if ref.Role == "ssh_key" && ref.RefName == SSHKeyRefSuffix("MYCONN_TOKEN") {
				found = true
			}
		}
		if !found {
			t.Errorf("git kind %q resolves no ssh_key secret ref", kind)
		}
	}
}
