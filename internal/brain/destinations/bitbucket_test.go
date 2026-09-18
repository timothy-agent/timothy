package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func bitbucketMission(t *testing.T) missions.Mission {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/wt", 0o750); err != nil {
		t.Fatal(err)
	}
	return missions.Mission{
		ID: "m1", Kind: "coding", Workspace: dir, Branch: "mission/x",
		Sources: []missions.SourceEntry{{Source: missions.SourceKindBitbucket, RepoURL: "https://bitbucket.org/acme/widgets.git", ConnectorID: "bb1"}},
	}
}

func TestBitbucketDeliverMissionModes(t *testing.T) {
	t.Parallel()
	resolveToken := func(context.Context, string) (string, error) { return "tok", nil }

	t.Run("push records branch and host", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		p := &fakePusher{host: "bitbucket.org"}
		a := &BitbucketAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: &fakePRSource{repoExists: true}}
		e := &missions.DestinationEntry{RepoURL: "https://bitbucket.org/acme/widgets.git"}

		if err := a.DeliverMission(context.Background(), GitHubConfig{ConnectorID: "bb1", Mode: "push"}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.Branch != "mission/x" || e.RemoteHost != "bitbucket.org" || e.PRURL != "" {
			t.Fatalf("entry after push = %+v", e)
		}
	})

	t.Run("push_pr records pr url and number", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		m.Name = "Widget Pricing Fix"
		ev := &fakeEvents{}
		pr := &fakePRSource{repoExists: true, defaultBranch: "main", prURL: "https://bitbucket.org/acme/widgets/pull-requests/7", prNumber: 7}
		a := &BitbucketAdapter{Pusher: &fakePusher{host: "bitbucket.org"}, Events: ev, ResolveToken: resolveToken, PR: pr}
		e := &missions.DestinationEntry{RepoURL: "https://bitbucket.org/acme/widgets.git"}

		if err := a.DeliverMission(context.Background(), GitHubConfig{ConnectorID: "bb1", Mode: "push_pr"}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.PRURL != "https://bitbucket.org/acme/widgets/pull-requests/7" || e.PRNumber != 7 {
			t.Fatalf("entry after push_pr = %+v", e)
		}
		if pr.lastTitle != "feat: widget pricing fix" {
			t.Fatalf("PR title = %q", pr.lastTitle)
		}
		var kinds []string
		for _, evt := range ev.events {
			kinds = append(kinds, evt.Kind)
		}
		if strings.Join(kinds, ",") != "mission.pushed,mission.pr_opened" {
			t.Fatalf("events = %v", kinds)
		}
	})

	t.Run("github url on a bitbucket destination is rejected", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		a := &BitbucketAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: &fakePRSource{repoExists: true}}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/acme/widgets.git"}

		err := a.DeliverMission(context.Background(), GitHubConfig{ConnectorID: "bb1", Mode: "push_pr"}, m, e)
		if err == nil || !strings.Contains(err.Error(), "bitbucket https clone URL") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("push failure surfaces", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		a := &BitbucketAdapter{Pusher: &fakePusher{err: missions.ErrPushRejected}, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: &fakePRSource{repoExists: true}}
		e := &missions.DestinationEntry{RepoURL: "https://bitbucket.org/acme/widgets.git"}
		if err := a.DeliverMission(context.Background(), GitHubConfig{ConnectorID: "bb1", Mode: "push"}, m, e); !errors.Is(err, missions.ErrPushRejected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unknown mode", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		a := &BitbucketAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: &fakePRSource{repoExists: true}}
		e := &missions.DestinationEntry{RepoURL: "https://bitbucket.org/acme/widgets.git"}
		if err := a.DeliverMission(context.Background(), GitHubConfig{ConnectorID: "bb1", Mode: "merge"}, m, e); err == nil {
			t.Fatal("want an error for an unknown mode")
		}
	})
}

func TestBitbucketEnsureRepo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		repoURL         string
		createIfMissing bool
		pr              *fakePRSource
		wantURL         string
		wantUpdated     bool
		wantErr         string
		wantCreateCalls int
		wantOrigin      int
	}{
		{name: "exists", repoURL: "https://bitbucket.org/acme/widgets.git", pr: &fakePRSource{repoExists: true}, wantURL: "https://bitbucket.org/acme/widgets.git", wantOrigin: 1},
		{name: "exists, browser url is canonicalised", repoURL: "https://bitbucket.org/acme/widgets/src/main/", pr: &fakePRSource{repoExists: true},
			wantURL: "https://bitbucket.org/acme/widgets.git", wantUpdated: true, wantOrigin: 1},
		{name: "missing, create", repoURL: "https://bitbucket.org/acme/widgets.git", createIfMissing: true,
			pr: &fakePRSource{newCloneURL: "https://bitbucket.org/acme/widgets-2.git"}, wantURL: "https://bitbucket.org/acme/widgets-2.git", wantUpdated: true, wantCreateCalls: 1},
		{name: "missing, no create", repoURL: "https://bitbucket.org/acme/widgets.git", pr: &fakePRSource{}, wantErr: "does not exist and create_if_missing is not set"},
		// bitbucketNames holds the adapter to bitbucketSource's real name
		// contract: a bare mission slug resolves against the connector's
		// configured workspace, and fails loudly without one (issue #786).
		{name: "no url, create from goal", createIfMissing: true,
			pr:      &fakePRSource{bitbucketNames: true, workspace: "acme", newCloneURL: "https://bitbucket.org/acme/fix-widgets.git"},
			wantURL: "https://bitbucket.org/acme/fix-widgets.git", wantUpdated: true, wantCreateCalls: 1},
		// Bitbucket hands back the user@ form for a user principal, which
		// validateRemote rejects, so it must be canonicalised before it
		// reaches SetOrigin (issue #787).
		{name: "created repo returns the user@ clone form", createIfMissing: true,
			pr:      &fakePRSource{newCloneURL: "https://sumon@bitbucket.org/acme/fix-widgets.git"},
			wantURL: "https://bitbucket.org/acme/fix-widgets.git", wantUpdated: true, wantCreateCalls: 1, wantOrigin: 1},
		{name: "existing target, created repo returns the user@ clone form", repoURL: "https://bitbucket.org/acme/widgets.git", createIfMissing: true,
			pr:      &fakePRSource{newCloneURL: "https://sumon@bitbucket.org/acme/widgets-2.git"},
			wantURL: "https://bitbucket.org/acme/widgets-2.git", wantUpdated: true, wantCreateCalls: 1, wantOrigin: 1},
		{name: "no url, create from goal, connector has no workspace", createIfMissing: true,
			pr:      &fakePRSource{bitbucketNames: true, newCloneURL: "https://bitbucket.org/acme/fix-widgets.git"},
			wantErr: "needs a workspace/slug name", wantCreateCalls: 1},
		{name: "no url, no create", pr: &fakePRSource{}, wantURL: ""},
		{name: "github url", repoURL: "https://github.com/acme/widgets.git", pr: &fakePRSource{}, wantErr: "bitbucket https clone URL"},
		{name: "existence check fails hard", repoURL: "https://bitbucket.org/acme/widgets.git", pr: &fakePRSource{existsErr: errors.New("boom")}, wantErr: "check existence: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := bitbucketMission(t)
			m.Goal = "fix widgets"
			p := &fakePusher{}
			a := &BitbucketAdapter{Pusher: p, Events: &fakeEvents{}, PR: tc.pr}
			got, updated, err := a.ensureRepo(context.Background(), m, "bb1", tc.repoURL, tc.createIfMissing)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureRepo: %v", err)
			}
			if got != tc.wantURL || updated != tc.wantUpdated {
				t.Fatalf("ensureRepo = (%q, %v), want (%q, %v)", got, updated, tc.wantURL, tc.wantUpdated)
			}
			wantOrigin := tc.wantOrigin
			if wantOrigin == 0 {
				wantOrigin = tc.wantCreateCalls
			}
			if tc.pr.createRepoCalls != tc.wantCreateCalls || p.setOriginCalls != wantOrigin {
				t.Fatalf("createRepo calls = %d (want %d), setOrigin calls = %d (want %d)", tc.pr.createRepoCalls, tc.wantCreateCalls, p.setOriginCalls, wantOrigin)
			}
		})
	}
}

// Both repo kinds route to their own adapter; a half-done switch would
// send a bitbucket row into the github adapter, which then rejects the URL.
func TestDeliverRoutesEachRepoKindToItsAdapter(t *testing.T) {
	m := bitbucketMission(t)
	destStore := &fakeDestStore{rows: map[string]Destination{
		"gh": {ID: "gh", Name: "gh-1", Kind: "github", Enabled: true, Config: json.RawMessage(`{"connector_id":"gh1","mode":"push"}`)},
		"bb": {ID: "bb", Name: "bb-1", Kind: "bitbucket", Enabled: true, Config: json.RawMessage(`{"connector_id":"bb1","mode":"push"}`)},
	}}
	eventStore := &fakeEventStore{}
	resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
	ghPush := &fakePusher{host: "github.com"}
	bbPush := &fakePusher{host: "bitbucket.org"}
	d := &Deliverer{
		store: destStore, events: eventStore, adapters: map[string]Adapter{}, log: discardLog(),
		github:    &GitHubAdapter{Pusher: ghPush, Events: eventStore, ResolveToken: resolveToken, PR: &fakePRSource{repoExists: true}},
		bitbucket: &BitbucketAdapter{Pusher: bbPush, Events: eventStore, ResolveToken: resolveToken, PR: &fakePRSource{repoExists: true}},
	}

	updated, err := d.Deliver(t.Context(), m, []missions.DestinationEntry{
		{DestinationID: "bb", RepoURL: "https://bitbucket.org/acme/widgets.git"},
		{DestinationID: "gh", RepoURL: "https://github.com/acme/widgets.git"},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if bbPush.pushCalls != 1 || ghPush.pushCalls != 1 {
		t.Fatalf("push calls: bitbucket %d, github %d, want 1 each", bbPush.pushCalls, ghPush.pushCalls)
	}
	for _, e := range updated {
		if e.DeliveredAt == "" {
			t.Fatalf("entry %s not delivered: %+v", e.DestinationID, e)
		}
	}
	if updated[0].RemoteHost != "bitbucket.org" || updated[1].RemoteHost != "github.com" {
		t.Fatalf("hosts = %q, %q", updated[0].RemoteHost, updated[1].RemoteHost)
	}
}

func TestDeliverBitbucketNoAdapterFails(t *testing.T) {
	m := bitbucketMission(t)
	destStore := &fakeDestStore{rows: map[string]Destination{
		"bb": {ID: "bb", Name: "bb-1", Kind: "bitbucket", Enabled: true, Config: json.RawMessage(`{"connector_id":"bb1","mode":"push"}`)},
	}}
	d := &Deliverer{store: destStore, events: &fakeEventStore{}, adapters: map[string]Adapter{}, log: discardLog()}
	if _, err := d.Deliver(t.Context(), m, entries("bb")); err == nil {
		t.Fatal("want an error with no bitbucket adapter wired")
	}
}
