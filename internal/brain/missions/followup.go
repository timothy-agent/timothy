package missions

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

const (
	// maxFollowUpAttachments caps how many parent workspace files one
	// follow-up carries, mirroring api/missions.go's maxMissionAttachments.
	maxFollowUpAttachments = 8
	// maxFollowUpAttachmentBytes caps one carried file's size.
	maxFollowUpAttachmentBytes = 8 << 20
)

// Brief is the structured hand-off a caller can attach to a follow-up:
// rendered into the child's discover/plan/work prompts as referenced
// context, alongside the parent outcome summary.
type Brief struct {
	Objective          string
	Scope              string
	NonGoals           string
	AcceptanceCriteria []string
	References         []string
}

// IsZero reports whether the brief renders nothing: every field blank.
func (b Brief) IsZero() bool {
	return b.Render() == ""
}

// Render formats the brief as prompt text, skipping empty fields.
func (b Brief) Render() string {
	var sb strings.Builder
	writeField := func(label, value string) {
		if v := strings.TrimSpace(value); v != "" {
			fmt.Fprintf(&sb, "%s: %s\n", label, v)
		}
	}
	writeList := func(label string, values []string) {
		var items []string
		for _, v := range values {
			if v := strings.TrimSpace(v); v != "" {
				items = append(items, v)
			}
		}
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&sb, "%s:\n", label)
		for _, item := range items {
			fmt.Fprintf(&sb, "- %s\n", item)
		}
	}
	writeField("Objective", b.Objective)
	writeField("Scope", b.Scope)
	writeField("Non-goals", b.NonGoals)
	writeList("Acceptance criteria", b.AcceptanceCriteria)
	writeList("References", b.References)
	return strings.TrimRight(sb.String(), "\n")
}

// FollowUpOptions carries a follow-up's own goal plus the optional
// parent workspace files to carry over and the structured brief.
type FollowUpOptions struct {
	Goal   string
	Attach []string
	Brief  Brief
}

// CreateFollowUp spawns a new mission continuing a terminal parent —
// the driver-layer counterpart of api/missions.go's create handler's
// own ParentMissionID branch, reused by builtin.FollowupMission so a
// chat-triggered follow-up can never diverge in behavior from one
// created through the mission-create API.
func (d *Driver) CreateFollowUp(ctx context.Context, parentID string, opts FollowUpOptions) (string, error) {
	goal := strings.TrimSpace(opts.Goal)
	if goal == "" {
		return "", fmt.Errorf("create follow-up: goal is required")
	}
	parent, err := d.store.Get(ctx, parentID)
	if err != nil {
		return "", fmt.Errorf("create follow-up: parent mission %s not found: %w", parentID, err)
	}
	if !parent.Phase.Terminal() {
		return "", fmt.Errorf("create follow-up: parent mission %s is not finished (phase %s)", parentID, parent.Phase)
	}
	events, err := d.store.Events(ctx, parentID)
	if err != nil {
		return "", fmt.Errorf("create follow-up: read parent events: %w", err)
	}
	// Resolved before Create so a bad attach path fails with no mission.
	attachments, err := resolveFollowUpAttachments(parent, opts.Attach)
	if err != nil {
		return "", fmt.Errorf("create follow-up: %w", err)
	}
	parentSource := SourceEntry{Source: SourceKindMission, ID: ParentLineageID, MissionID: parent.ID, Digest: OutcomeDigest(parent, events, parent.Phase, parent.FailureReason)}
	var sources []SourceEntry
	sources = append(sources, parentSource)
	if github, ok := parent.GitHubSource(); ok {
		sources = append(sources, github)
	}
	if !opts.Brief.IsZero() {
		sources = append(sources, SourceEntry{Source: SourceKindBrief, Name: "Brief", Digest: opts.Brief.Render()})
	}
	sources = append(sources, attachments...)

	child := Mission{
		Goal: goal, Kind: parent.Kind, AgentID: parent.AgentID,
		Route: parent.Route, ReviewRoute: parent.ReviewRoute, PlanRoute: parent.PlanRoute,
		EscalationRoute: parent.EscalationRoute, MaxIterations: parent.MaxIterations,
		// Pins name an entry inside a route, so they carry over with the
		// routes above; dropping them would silently demote a follow-up
		// to the chain's first usable entry.
		RouteModel: parent.RouteModel, PlanRouteModel: parent.PlanRouteModel,
		ReviewRouteModel: parent.ReviewRouteModel,
		BudgetAmount:     parent.BudgetAmount, BudgetCurrency: parent.BudgetCurrency,
		AutoApproveTools: parent.AutoApproveTools, AutoApprovePlan: parent.AutoApprovePlan, PromptOverlay: parent.PromptOverlay,
		Harness: parent.Harness, Environment: parent.Environment,
		Flow: parent.Flow, ParentMissionID: parent.ID, Sources: sources,
		// Deliberately NOT copied from parent: Destinations (push consent,
		// destination_ids, and kb promotion are per-mission human choices,
		// D-061, operator addresses outputs per mission), pdf sources (a
		// follow-up's own documents, not the parent's, beyond the files
		// the caller explicitly asked to carry over).
	}
	id, err := d.Create(ctx, child)
	if err != nil {
		return "", fmt.Errorf("create follow-up: %w", err)
	}
	// Fire-and-forget display name generation, same shape as
	// api/missions.go's create handler's own generateName — detached
	// from ctx so a caller winding down doesn't cancel it.
	if d.nameMission != nil {
		go d.backfillMissionName(context.Background(), id, goal) //nolint:gosec // G118: deliberate — the naming call must outlive whatever request/ctx triggered create
	}
	return id, nil
}

// resolveFollowUpAttachments turns parent workspace-relative paths into
// "pdf" source entries carrying the file's markdown plus the parent id
// the provisioner copies the bytes from. Every path must resolve to a
// regular file inside the parent's workspace.
func resolveFollowUpAttachments(parent Mission, paths []string) ([]SourceEntry, error) {
	var rels []string
	seen := map[string]bool{}
	for _, p := range paths {
		rel := strings.TrimSpace(p)
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		rels = append(rels, rel)
	}
	if len(rels) == 0 {
		return nil, nil
	}
	if len(rels) > maxFollowUpAttachments {
		return nil, fmt.Errorf("attach: at most %d files, got %d", maxFollowUpAttachments, len(rels))
	}
	if parent.Workspace == "" {
		return nil, fmt.Errorf("attach: parent mission has no workspace")
	}
	workRoot := parent.WorkRoot()
	var out []SourceEntry
	for _, rel := range rels {
		if filepath.IsAbs(rel) {
			return nil, fmt.Errorf("attach %q: paths must be relative to the parent mission's workspace", rel)
		}
		cleaned := filepath.Clean(rel)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("attach %q: outside the parent mission's workspace", rel)
		}
		entry, err := readFollowUpAttachment(parent, workRoot, cleaned)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// readFollowUpAttachment reads one parent workspace file into a "pdf"
// source entry.
func readFollowUpAttachment(parent Mission, workRoot, rel string) (SourceEntry, error) {
	f, fi, err := OpenFile(workRoot, rel)
	if err != nil {
		if tools.IsViolation(err) {
			return SourceEntry{}, fmt.Errorf("attach %q: outside the parent mission's workspace", rel)
		}
		return SourceEntry{}, fmt.Errorf("attach %q: not found in the parent mission's workspace", rel)
	}
	defer func() { _ = f.Close() }()
	if fi.Size() > maxFollowUpAttachmentBytes {
		return SourceEntry{}, fmt.Errorf("attach %q: file is larger than %d bytes", rel, maxFollowUpAttachmentBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFollowUpAttachmentBytes+1))
	if err != nil {
		return SourceEntry{}, fmt.Errorf("attach %q: read: %w", rel, err)
	}
	if len(data) > maxFollowUpAttachmentBytes {
		return SourceEntry{}, fmt.Errorf("attach %q: file is larger than %d bytes", rel, maxFollowUpAttachmentBytes)
	}
	markdown := ""
	if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
		markdown = markitdown.TruncateMarkdown(string(data))
	}
	id, mime := "", followUpAttachmentMime(rel, markdown != "")
	for _, ref := range parent.ArtifactRefs {
		if ref.Name == rel {
			id = ref.ID
			if ref.Mime != "" {
				mime = ref.Mime
			}
			break
		}
	}
	return SourceEntry{Source: SourceKindPDF, ID: id, Mime: mime, Name: rel, Markdown: markdown, MissionID: parent.ID}, nil
}

// followUpAttachmentMime guesses a carried file's mime from its
// extension, falling back to whether its bytes were readable as text.
func followUpAttachmentMime(rel string, isText bool) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md", ".markdown":
		return "text/markdown"
	}
	if isText {
		return "text/plain"
	}
	return "application/octet-stream"
}
