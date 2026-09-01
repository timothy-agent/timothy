package connectors

import "testing"

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

func TestConnectorSecretRefsGitHubSigningKey(t *testing.T) {
	c := Connector{
		Kind: "github", CredentialRef: "MYCONN_PAT",
		Config: []byte(`{"sign_commits":true}`),
	}
	got := c.SecretRefs()
	want := []SecretRefRole{
		{RefName: "MYCONN_PAT", Role: "credential"},
		{RefName: SigningKeyRefSuffix("MYCONN_PAT"), Role: "signing_key"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SecretRefs() = %+v, want %+v", got, want)
	}
}
