//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSetEnvironmentPersistsToolchains: SetEnvironment writes the
// toolchains column and the event payload carries them; SetToolchains
// updates the column alone.
func TestSetEnvironmentPersistsToolchains(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "toolchains", Kind: KindCoding, Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m, err := s.Get(ctx, id); err != nil || len(m.Toolchains) != 0 {
		t.Fatalf("Get after Create = %v, %v, want no toolchains", m.Toolchains, err)
	}
	want := map[string]string{"python": "3.10"}
	if err := s.SetEnvironment(ctx, id, "python", "pyproject.toml", nil, want); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}
	if m, _ := s.Get(ctx, id); !reflect.DeepEqual(m.Toolchains, want) {
		t.Fatalf("Toolchains = %v, want %v", m.Toolchains, want)
	}
	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Kind != "mission.environment_detected" {
			continue
		}
		found = true
		var p struct {
			Toolchains map[string]string `json:"toolchains"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil || !reflect.DeepEqual(p.Toolchains, want) {
			t.Fatalf("payload toolchains = %v, %v, want %v", p.Toolchains, err, want)
		}
	}
	if !found {
		t.Fatal("no mission.environment_detected event")
	}
	if err := s.SetEnvironment(ctx, id, "node", "discover", nil, nil); err != nil {
		t.Fatalf("SetEnvironment override: %v", err)
	}
	if m, _ := s.Get(ctx, id); len(m.Toolchains) != 0 {
		t.Fatalf("Toolchains after override with none = %v, want cleared", m.Toolchains)
	}
	if err := s.SetToolchains(ctx, id, map[string]string{"node": "18"}); err != nil {
		t.Fatalf("SetToolchains: %v", err)
	}
	if m, _ := s.Get(ctx, id); !reflect.DeepEqual(m.Toolchains, map[string]string{"node": "18"}) {
		t.Fatalf("Toolchains = %v, want node 18", m.Toolchains)
	}
}

// stubMise puts a mise on PATH that exits 1 for any python@0.0.1 and
// 0 otherwise, so the install runs through the real /bin/sh path.
func stubMise(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in *@0.0.1) echo \"mise ERROR no version matching $a\" >&2; exit 1;; esac; done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(stub), 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func provisionWithRepo(t *testing.T, files map[string]string) (Mission, *Store) {
	t.Helper()
	bare := toolchainRepo(t, files)
	store := testStore(t)
	ctx := context.Background()
	id, err := store.Create(ctx, Mission{
		Goal: marker + "toolchain provision", Kind: KindCoding, Route: "default",
		Sources: []SourceEntry{{Source: SourceKindGitHub, RepoURL: bare, ConnectorID: "conn1"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	d := NewDriver(store, &scriptedRunner{}, NewWorkspace(t.TempDir(), nil, log), nil, nil, fakeSandboxExec, nil, log)
	d.SetCloneTokenResolver(func(context.Context, string) (string, error) { return "dummy-token", nil })
	m, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.provision.ensureProvisioned(ctx, m); err != nil {
		t.Fatalf("ensureProvisioned: %v", err)
	}
	m, err = store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return m, store
}

func TestProvisionInstallsToolchain(t *testing.T) {
	stubMise(t)
	m, store := provisionWithRepo(t, map[string]string{"pyproject.toml": "[project]\nname='x'\n", ".python-version": "3.10\n"})
	if !reflect.DeepEqual(m.Toolchains, map[string]string{"python": "3.10"}) {
		t.Fatalf("Toolchains = %v, want python 3.10", m.Toolchains)
	}
	events, _ := store.Events(context.Background(), m.ID)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if !strings.Contains(strings.Join(kinds, ","), "mission.toolchain_installed") {
		t.Fatalf("events = %v, want mission.toolchain_installed", kinds)
	}
}

func TestProvisionToolchainInstallFailureEvent(t *testing.T) {
	stubMise(t)
	m, store := provisionWithRepo(t, map[string]string{"pyproject.toml": "x", ".python-version": "0.0.1\n"})
	if m.Workspace == "" {
		t.Fatal("mission not provisioned after a failed install")
	}
	events, _ := store.Events(context.Background(), m.ID)
	for _, e := range events {
		if e.Kind != "mission.toolchain_install_failed" {
			continue
		}
		var p struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil || !strings.Contains(p.Error, "python@0.0.1") {
			t.Fatalf("failure payload = %s, want mise stderr naming python@0.0.1", e.Payload)
		}
		return
	}
	t.Fatal("no mission.toolchain_install_failed event")
}

// TestProvisionPHPToolchain: a php repo records the newest baked minor
// its composer constraint allows (D-139), and a minor the image lacks
// yields a failure event naming it while the mission still provisions.
// The selection itself runs through the real
// /bin/sh; this container has no /usr/bin/php7.4.
func TestProvisionPHPToolchain(t *testing.T) {
	m, _ := provisionWithRepo(t, map[string]string{"composer.json": `{"require":{"php":"^8.0.2"}}`})
	if m.Environment != "php" || !reflect.DeepEqual(m.Toolchains, map[string]string{"php": "8.4"}) {
		t.Fatalf("env = %q, Toolchains = %v; want php with php 8.4", m.Environment, m.Toolchains)
	}
	m, store := provisionWithRepo(t, map[string]string{"composer.json": `{"require":{"php":"7.4.33"}}`})
	if m.Workspace == "" {
		t.Fatal("mission not provisioned after a failed php select")
	}
	events, _ := store.Events(context.Background(), m.ID)
	for _, e := range events {
		if e.Kind == "mission.toolchain_install_failed" {
			if !strings.Contains(string(e.Payload), "php 7.4 is not installed") {
				t.Fatalf("failure payload = %s, want it to name php 7.4", e.Payload)
			}
			return
		}
	}
	t.Fatal("no mission.toolchain_install_failed event")
}
