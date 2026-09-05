package destinations

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeEvents records every AppendEvent call for assertion, standing in
// for *missions.Store without a real Postgres pool.
type fakeEvents struct {
	mu     sync.Mutex
	events []missions.Event
	err    error
}

func (f *fakeEvents) AppendEvent(_ context.Context, id, kind string, _ map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, missions.Event{MissionID: id, Kind: kind})
	return nil
}

// fakePRSource scripts PRSource for DeliverMission's push_pr path
// without a real GitHub connector. repoExists/existsErr/
// newCloneURL/createRepoErr back ensureRepo's create-if-missing path
// (issue #483); existsCalls/createRepoCalls count those specifically,
// distinct from createCalls (CreatePR, the pull-request call).
type fakePRSource struct {
	defaultBranch string
	defaultErr    error
	prURL         string
	prNumber      int
	createErr     error
	createCalls   int

	repoExists      bool
	existsErr       error
	existsCalls     int
	newCloneURL     string
	createRepoErr   error
	createRepoCalls int
}

func (f *fakePRSource) DefaultBranch(_ context.Context, _, _, _ string) (string, error) {
	return f.defaultBranch, f.defaultErr
}

func (f *fakePRSource) CreatePR(_ context.Context, _, _, _, _, _, _, _ string) (string, int, error) {
	f.createCalls++
	if f.createErr != nil {
		return "", 0, f.createErr
	}
	return f.prURL, f.prNumber, nil
}

func (f *fakePRSource) RepoExists(_ context.Context, _, _, _ string) (bool, error) {
	f.existsCalls++
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.repoExists, nil
}

func (f *fakePRSource) CreateRepo(_ context.Context, _, _ string, _ bool) (string, error) {
	f.createRepoCalls++
	if f.createRepoErr != nil {
		return "", f.createRepoErr
	}
	return f.newCloneURL, nil
}

// fakePusher scripts pusher, the https-only remote validation
// Workspace.Push itself enforces is missions' own push_test.go
// coverage; GitHubAdapter's job (proven here) is what it does with a
// push that succeeds or fails, not git plumbing. setOriginCalls/
// setOriginErr back ensureRepo's create-if-missing path (issue #483).
type fakePusher struct {
	host      string
	err       error
	pushCalls int

	setOriginCalls int
	setOriginErr   error
	lastOriginURL  string
}

func (f *fakePusher) Push(_ context.Context, _, _, _ string) (string, error) {
	f.pushCalls++
	if f.err != nil {
		return "", f.err
	}
	return f.host, nil
}

func (f *fakePusher) SetOrigin(_ context.Context, _, remoteURL string) error {
	f.setOriginCalls++
	f.lastOriginURL = remoteURL
	return f.setOriginErr
}

// pushableMission is a coding mission with the guards missions.NotPushable
// requires already satisfied (worktree present, branch set); t.TempDir()
// stands in for a real workspace since fakePusher never touches the
// filesystem; WorktreePath() derives workspace/wt from it.
func pushableMission(t *testing.T) missions.Mission {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/wt", 0o750); err != nil {
		t.Fatal(err)
	}
	return missions.Mission{
		ID: "m1", Kind: "coding", Workspace: dir, Branch: "mission/x",
		Sources: []missions.SourceEntry{{Source: missions.SourceKindGitHub, RepoURL: "https://github.com/octo/repo.git", ConnectorID: "conn1"}},
	}
}

// TestEnsureRepo table-tests ensureRepo's create-if-missing decision
// (issue #483): repo already exists, repo missing with
// create_if_missing=true, repo missing with create_if_missing=false,
// and the retry-after-already-created idempotency path (RepoExists now
// reports true because a prior attempt already created it).
func TestEnsureRepo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// repoURL is set for every case except the "no repo_url at all,
		// derive from goal" case.
		repoURL         string
		createIfMissing bool
		repoExists      bool
		existsErr       error
		createErr       error
		newCloneURL     string

		wantErr           bool
		wantUpdated       bool
		wantCreateCalls   int
		wantSetOriginCall int
		wantFinalRepoURL  string
	}{
		{
			name:              "repo exists: no-op",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   true,
			repoExists:        true,
			wantErr:           false,
			wantUpdated:       false,
			wantCreateCalls:   0,
			wantSetOriginCall: 0,
			wantFinalRepoURL:  "https://github.com/octo/repo.git",
		},
		{
			name:              "repo missing, create_if_missing=true: creates and redirects origin",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   true,
			repoExists:        false,
			newCloneURL:       "https://github.com/octo/repo.git",
			wantErr:           false,
			wantUpdated:       true,
			wantCreateCalls:   1,
			wantSetOriginCall: 1,
			wantFinalRepoURL:  "https://github.com/octo/repo.git",
		},
		{
			name:              "repo missing, create_if_missing=false: fails honestly, never creates",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   false,
			repoExists:        false,
			wantErr:           true,
			wantUpdated:       false,
			wantCreateCalls:   0,
			wantSetOriginCall: 0,
		},
		{
			name:              "retry after already created: idempotent, no second create",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   true,
			repoExists:        true, // a prior attempt already created it
			wantErr:           false,
			wantUpdated:       false,
			wantCreateCalls:   0,
			wantSetOriginCall: 0,
			wantFinalRepoURL:  "https://github.com/octo/repo.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := &fakePRSource{repoExists: tc.repoExists, existsErr: tc.existsErr, newCloneURL: tc.newCloneURL, createRepoErr: tc.createErr}
			p := &fakePusher{}
			a := &GitHubAdapter{Pusher: p, PR: pr}
			m := pushableMission(t)

			got, updated, err := a.ensureRepo(context.Background(), m, "conn1", tc.repoURL, tc.createIfMissing)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ensureRepo error = %v, wantErr %v", err, tc.wantErr)
			}
			if updated != tc.wantUpdated {
				t.Fatalf("updated = %v, want %v", updated, tc.wantUpdated)
			}
			if pr.createRepoCalls != tc.wantCreateCalls {
				t.Fatalf("CreateRepo called %d times, want %d", pr.createRepoCalls, tc.wantCreateCalls)
			}
			if p.setOriginCalls != tc.wantSetOriginCall {
				t.Fatalf("SetOrigin called %d times, want %d", p.setOriginCalls, tc.wantSetOriginCall)
			}
			if !tc.wantErr && got != tc.wantFinalRepoURL {
				t.Fatalf("final repoURL = %q, want %q", got, tc.wantFinalRepoURL)
			}
		})
	}
}

// TestEnsureRepoNoRepoURLDerivesNameFromGoal proves an entry with no
// repoURL at all but createIfMissing=true derives a repo name from the
// mission's goal (via Slug) instead of failing: the "brand new scratch
// mission gets its own repo" case.
func TestEnsureRepoNoRepoURLDerivesNameFromGoal(t *testing.T) {
	t.Parallel()
	pr := &fakePRSource{newCloneURL: "https://github.com/octo/fix-the-thing.git"}
	p := &fakePusher{}
	a := &GitHubAdapter{Pusher: p, PR: pr}
	m := pushableMission(t)
	m.Goal = "fix the thing"

	got, updated, err := a.ensureRepo(context.Background(), m, "conn1", "", true)
	if err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	if !updated {
		t.Fatal("expected updated=true")
	}
	if got != "https://github.com/octo/fix-the-thing.git" {
		t.Fatalf("repoURL = %q, want the created clone URL", got)
	}
	if pr.existsCalls != 0 {
		t.Fatalf("RepoExists called %d times, want 0 (no owner/repo to check without a name)", pr.existsCalls)
	}
	if pr.createRepoCalls != 1 {
		t.Fatalf("CreateRepo called %d times, want 1", pr.createRepoCalls)
	}
}

// TestDeliverMissionRepoResolution table-tests DeliverMission's repo
// resolution order (issue #560): (a) the entry's own RepoURL, (b) the
// mission's own clone-source RepoURL, (c) create-if-missing when
// neither is set, (d) failure when none of the above apply.
func TestDeliverMissionRepoResolution(t *testing.T) {
	t.Parallel()

	t.Run("a: entry RepoURL wins", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t) // clone source is octo/repo
		p := &fakePusher{host: "github.com"}
		pr := &fakePRSource{repoExists: true}
		resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
		a := &GitHubAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: pr}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/entry-repo.git"}
		cfg := GitHubConfig{ConnectorID: "conn1", Mode: "push"}

		if err := a.DeliverMission(context.Background(), cfg, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.RepoURL != "https://github.com/octo/entry-repo.git" {
			t.Fatalf("RepoURL = %q, want the entry's own repo", e.RepoURL)
		}
		if e.Branch != "mission/x" || e.RemoteHost != "github.com" {
			t.Fatalf("entry after delivery = %+v", e)
		}
	})

	t.Run("b: falls back to mission clone source", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t) // clone source is octo/repo
		p := &fakePusher{host: "github.com"}
		pr := &fakePRSource{repoExists: true}
		resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
		a := &GitHubAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: pr}
		e := &missions.DestinationEntry{}
		cfg := GitHubConfig{ConnectorID: "conn1", Mode: "push"}

		if err := a.DeliverMission(context.Background(), cfg, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.RepoURL != m.RepoURL() {
			t.Fatalf("RepoURL = %q, want the mission's clone source %q", e.RepoURL, m.RepoURL())
		}
	})

	t.Run("c: create_if_missing derives a repo", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		m.Sources = nil // no clone source at all -- a scratch mission
		p := &fakePusher{host: "github.com"}
		pr := &fakePRSource{newCloneURL: "https://github.com/octo/fix-the-thing.git"}
		resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
		a := &GitHubAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: pr}
		e := &missions.DestinationEntry{}
		cfg := GitHubConfig{ConnectorID: "conn1", Mode: "push", CreateIfMissing: true}

		if err := a.DeliverMission(context.Background(), cfg, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.RepoURL != "https://github.com/octo/fix-the-thing.git" {
			t.Fatalf("RepoURL = %q, want the created repo", e.RepoURL)
		}
		if pr.createRepoCalls != 1 {
			t.Fatalf("CreateRepo called %d times, want 1", pr.createRepoCalls)
		}
	})

	t.Run("d: no repo and create_if_missing off fails", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		m.Sources = nil
		resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
		a := &GitHubAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken}
		e := &missions.DestinationEntry{}
		cfg := GitHubConfig{ConnectorID: "conn1", Mode: "push"}

		if err := a.DeliverMission(context.Background(), cfg, m, e); err == nil {
			t.Fatal("DeliverMission with no repo and create_if_missing off: want an error, got nil")
		}
	})
}

// TestDeliverMissionModes covers push vs push_pr recording the right
// entry fields, and that a push failure leaves the Error path to the
// caller (deliverOne/recordOutcome) rather than swallowing it.
func TestDeliverMissionModes(t *testing.T) {
	t.Parallel()

	t.Run("push records branch and host", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		p := &fakePusher{host: "github.com"}
		pr := &fakePRSource{repoExists: true}
		resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
		a := &GitHubAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: pr}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}
		cfg := GitHubConfig{ConnectorID: "conn1", Mode: "push"}

		if err := a.DeliverMission(context.Background(), cfg, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.Branch != "mission/x" || e.RemoteHost != "github.com" || e.PRURL != "" {
			t.Fatalf("entry after push = %+v", e)
		}
	})

	t.Run("push_pr records branch, pr url and number", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		p := &fakePusher{host: "github.com"}
		pr := &fakePRSource{repoExists: true, defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/1", prNumber: 1}
		resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
		a := &GitHubAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: pr}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}
		cfg := GitHubConfig{ConnectorID: "conn1", Mode: "push_pr"}

		if err := a.DeliverMission(context.Background(), cfg, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.PRURL != "https://github.com/octo/repo/pull/1" || e.PRNumber != 1 {
			t.Fatalf("entry after push_pr = %+v", e)
		}
	})

	t.Run("push failure surfaces as an error", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		p := &fakePusher{err: missions.ErrPushRejected}
		pr := &fakePRSource{repoExists: true}
		resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
		a := &GitHubAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, PR: pr}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}
		cfg := GitHubConfig{ConnectorID: "conn1", Mode: "push"}

		if err := a.DeliverMission(context.Background(), cfg, m, e); err == nil {
			t.Fatal("DeliverMission with a rejected push: want an error, got nil")
		}
	})
}
