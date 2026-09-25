//go:build integration

package destinations

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func channelTestPool(t *testing.T) *pgpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = db.Exec(cctx, `DELETE FROM destinations WHERE name LIKE $1 || '%'`, marker)
		_, _ = db.Exec(cctx, `DELETE FROM channels WHERE name LIKE $1 || '%'`, marker)
		_, _ = db.Exec(cctx, `DELETE FROM missions WHERE goal LIKE $1 || '%'`, marker)
	})
	return pool
}

func storeChannelLookup(s *channels.Store) ChannelLookup {
	return func(ctx context.Context, id string) (ChannelRef, error) {
		c, err := s.Get(ctx, id)
		if err != nil {
			return ChannelRef{}, err
		}
		return ChannelRef{Kind: c.Kind, CredentialRef: c.CredentialRef, ConnectorID: c.Config.ConnectorID}, nil
	}
}

// TestChannelDestinationDelivers pins issue #831's destination: a
// mission delivers through telegram (MarkdownV2 in the topic), slack
// (files upload in the thread) and email channels (text plus files),
// validated against real channel rows; a disabled channel delivers.
func TestChannelDestinationDelivers(t *testing.T) {
	pool := channelTestPool(t)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tag := marker + time.Now().Format("150405.000000") + "-"
	cs := channels.NewStore(pool)
	newChannel := func(c channels.Channel) string {
		t.Helper()
		c.Name = tag + c.Name
		id, err := cs.Create(ctx, c)
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		return id
	}
	//nolint:gosec // G101: ref names, not credential values.
	tgID := newChannel(channels.Channel{Name: "tg", Kind: channels.KindTelegram, CredentialRef: "TG_REF", Enabled: false})
	slID := newChannel(channels.Channel{Name: "sl", Kind: channels.KindSlack, CredentialRef: "SLACK_BOT", Config: channels.Config{AppTokenRef: "SLACK_APP"}})
	emID := newChannel(channels.Channel{Name: "em", Kind: channels.KindEmail, Config: channels.Config{ConnectorID: "00000000-0000-0000-0000-00000000e001", FromAllow: []string{"@example.com"}}})

	ds := NewStore(pool, nil, storeChannelLookup(cs), log)
	newDest := func(name, config string) string {
		t.Helper()
		id, err := ds.Create(ctx, Destination{Name: tag + name, Kind: "channel", Enabled: true, Config: json.RawMessage(config)})
		if err != nil {
			t.Fatalf("create destination %s: %v", name, err)
		}
		return id
	}
	tgDest := newDest("tg", `{"channel_id":"`+tgID+`","chat_id":"-100","thread_id":"7"}`)
	slDest := newDest("sl", `{"channel_id":"`+slID+`","chat_id":"C0123","thread_id":"1700.1"}`)
	emDest := newDest("em", `{"channel_id":"`+emID+`","to":"ops@example.com"}`)
	if _, err := ds.Create(ctx, Destination{Name: tag + "bad", Kind: "channel", Config: json.RawMessage(`{"channel_id":"` + emID + `","chat_id":"1"}`)}); err == nil {
		t.Fatal("an email channel destination without to was accepted")
	}
	if _, err := ds.Create(ctx, Destination{Name: tag + "old", Kind: "telegram", CredentialRef: "TG_REF", Config: json.RawMessage(`{"chat_id":"1"}`)}); err == nil {
		t.Fatal("the retired telegram kind was accepted")
	}

	tg, sl := newFakeTelegram(t), newFakeSlack(t)
	var mails []sentMail
	adapter := testChannelAdapter(tg, sl, &mails)
	adapter.Lookup = storeChannelLookup(cs)
	adapter.ResolveToken = func(ctx context.Context, ref string) (string, error) {
		if ref == "TG_REF" {
			return "tg-secret", nil
		}
		return resolveTestToken(ctx, ref)
	}
	ms := missions.NewStore(pool, log)
	d := NewDeliverer(ds, ms, nil, &WebhookAdapter{}, adapter, nil, nil, nil, log)

	mission := func(name string, files map[string]string) missions.Mission {
		t.Helper()
		id, err := ms.Create(ctx, missions.Mission{Goal: tag + name, Name: name, Kind: "general", Route: "default"})
		if err != nil {
			t.Fatal(err)
		}
		m, err := ms.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		m.Workspace = t.TempDir()
		var unit missions.PlanUnit
		for rel, content := range files {
			if err := os.WriteFile(filepath.Join(m.Workspace, rel), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			unit.Artifacts = append(unit.Artifacts, rel)
		}
		m.Plan = missions.Plan{Units: []missions.PlanUnit{unit}}
		return m
	}
	entries := func(ids ...string) []missions.DestinationEntry {
		var out []missions.DestinationEntry
		for _, id := range ids {
			out = append(out, missions.DestinationEntry{DestinationID: id})
		}
		return out
	}

	digest := mission("digest", map[string]string{"digest.md": "# Digest\n\nsomething **important**."})
	out, err := d.Deliver(ctx, digest, entries(tgDest, emDest))
	if err != nil {
		t.Fatalf("Deliver digest: %v", err)
	}
	for _, e := range out {
		if e.DeliveredAt == "" || e.Error != "" {
			t.Fatalf("entry = %+v", e)
		}
	}
	if len(tg.messages) != 1 || len(tg.documents) != 0 {
		t.Fatalf("telegram messages=%d documents=%d", len(tg.messages), len(tg.documents))
	}
	msg := tg.messages[0]
	text, _ := msg["text"].(string)
	if msg["chat_id"] != "-100" || msg["message_thread_id"] != "7" || msg["parse_mode"] != "MarkdownV2" ||
		!strings.Contains(text, "*digest*") || !strings.Contains(text, "*Digest*") || !strings.Contains(text, "*important*") {
		t.Fatalf("telegram message = %+v", msg)
	}
	if len(mails) != 1 || mails[0].To != "ops@example.com" || mails[0].ConnectorID != "00000000-0000-0000-0000-00000000e001" || !strings.Contains(mails[0].Body, "something **important**.") {
		t.Fatalf("mails = %+v", mails)
	}

	export := mission("export", map[string]string{"data.csv": "a,b\n1,2\n"})
	if _, err := d.Deliver(ctx, export, entries(slDest, emDest)); err != nil {
		t.Fatalf("Deliver export: %v", err)
	}
	if got := string(sl.uploaded["F_data.csv"]); got != "a,b\n1,2\n" {
		t.Fatalf("slack upload = %q", got)
	}
	done := sl.callsOf("files.completeUploadExternal")
	if comment, _ := done[0].Body["initial_comment"].(string); len(done) != 1 || done[0].Body["channel_id"] != "C0123" || done[0].Body["thread_ts"] != "1700.1" || !strings.HasPrefix(comment, "*export*\n") {
		t.Fatalf("complete upload = %+v", done)
	}
	if len(mails) != 2 || len(mails[1].Files) != 1 || mails[1].Files[0].Name != "data.csv" {
		t.Fatalf("export mail = %+v", mails)
	}
	if n, err := ms.Events(ctx, export.ID); err != nil || len(n) != 2 || n[0].Kind != eventDelivered {
		t.Fatalf("export events = %+v %v", n, err)
	}
}
