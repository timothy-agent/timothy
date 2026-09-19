package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// Both repo kinds now route through ONE RepoAdapter (issue #795): the
// destination row's kind no longer picks an adapter, the connector the
// row names picks the Client. This proves a mixed pair still delivers,
// each against its own provider.
func TestDeliverRoutesEveryRepoKindThroughOneAdapter(t *testing.T) {
	m := bitbucketMission(t)
	destStore := &fakeDestStore{rows: map[string]Destination{
		"gh": {ID: "gh", Name: "gh-1", Kind: "github", Enabled: true, Config: json.RawMessage(`{"connector_id":"gh1","mode":"push"}`)},
		"bb": {ID: "bb", Name: "bb-1", Kind: "bitbucket", Enabled: true, Config: json.RawMessage(`{"connector_id":"bb1","mode":"push"}`)},
	}}
	eventStore := &fakeEventStore{}
	push := &fakePusher{host: "example.com"}
	// One Clients that answers per connector id, the way the real
	// Manager.GitClient does.
	byConnector := perConnectorClients{
		"gh1": githubClient(&fakeGitClient{repoExists: true}),
		"bb1": bitbucketClient(&fakeGitClient{repoExists: true}),
	}
	d := &Deliverer{
		store: destStore, events: eventStore, adapters: map[string]Adapter{}, log: discardLog(),
		repo: &RepoAdapter{Pusher: push, Events: eventStore, Clients: byConnector,
			ResolveToken: func(context.Context, string) (string, error) { return "tok", nil }},
	}

	updated, err := d.Deliver(t.Context(), m, []missions.DestinationEntry{
		{DestinationID: "bb", RepoURL: "https://bitbucket.org/acme/widgets.git"},
		{DestinationID: "gh", RepoURL: "https://github.com/acme/widgets.git"},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if push.pushCalls != 2 {
		t.Fatalf("push calls = %d, want 2 (one per destination)", push.pushCalls)
	}
	for _, e := range updated {
		if e.DeliveredAt == "" {
			t.Fatalf("entry %s not delivered: %+v", e.DestinationID, e)
		}
	}
	// Each entry kept its own provider's canonical clone URL, proving
	// neither was parsed by the other kind's Descriptor.
	if updated[0].RepoURL != "https://bitbucket.org/acme/widgets.git" || updated[1].RepoURL != "https://github.com/acme/widgets.git" {
		t.Fatalf("repo urls = %q, %q", updated[0].RepoURL, updated[1].RepoURL)
	}
}

// perConnectorClients resolves a different Client per connector id.
type perConnectorClients map[string]*fakeGitClient

func (p perConnectorClients) ClientFor(_ context.Context, connectorID string) (gitprovider.Client, func(), error) {
	c, ok := p[connectorID]
	if !ok {
		return nil, nil, errNoSuchConnector
	}
	return c, func() {}, nil
}

var errNoSuchConnector = errors.New("no such connector")

func TestDeliverRepoKindNoAdapterFails(t *testing.T) {
	m := bitbucketMission(t)
	destStore := &fakeDestStore{rows: map[string]Destination{
		"bb": {ID: "bb", Name: "bb-1", Kind: "bitbucket", Enabled: true, Config: json.RawMessage(`{"connector_id":"bb1","mode":"push"}`)},
	}}
	d := &Deliverer{store: destStore, events: &fakeEventStore{}, adapters: map[string]Adapter{}, log: discardLog()}
	if _, err := d.Deliver(t.Context(), m, entries("bb")); err == nil {
		t.Fatal("want an error with no repo adapter wired")
	}
}
