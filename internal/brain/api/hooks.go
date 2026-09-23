package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/events"
)

const (
	// hookBodyLimit caps an inbound webhook body.
	hookBodyLimit = 1 << 20
	// hookTimestampSkew bounds X-Timothy-Timestamp around now.
	hookTimestampSkew = 5 * time.Minute
	// hookBurst requests per hookRefill per trigger.
	hookBurst  = 30
	hookRefill = time.Minute
)

// registerHooks mounts POST /hooks/{trigger_id} (issue #827) without
// bearer auth: each delivery is verified against its trigger's HMAC
// key instead. Unmounted unless the automation store, the event store
// and the secret resolver are wired. notify is
// Notifier.NotifyOperator; nil skips the notification.
func (a *API) registerHooks(handle func(pattern string, h http.Handler), store *automations.Store, ev *events.Store, kick func(), resolveSecret func(context.Context, string) (string, error), notify func(ctx context.Context, kind, message string) error) {
	if store == nil || ev == nil || resolveSecret == nil {
		return
	}
	h := &hookAPI{store: store, events: ev, kick: kick, resolveSecret: resolveSecret, notify: notify,
		limiter: newHookLimiter(), log: a.log, now: time.Now}
	handle("POST /hooks/{trigger_id}", http.HandlerFunc(h.receive))
}

type hookAPI struct {
	store         *automations.Store
	events        *events.Store
	kick          func()
	resolveSecret func(context.Context, string) (string, error)
	notify        func(ctx context.Context, kind, message string) error
	limiter       *hookLimiter
	log           *slog.Logger
	now           func() time.Time
}

// hookError writes {"error": code} and nothing else, so no input is
// ever echoed back.
func hookError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

// receive verifies one delivery and records it as a webhook.received
// event. Every step fails closed and answers before any model work.
func (h *hookAPI) receive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("trigger_id")
	now := h.now()
	trigger, automation, err := h.store.WebhookTrigger(ctx, id, now)
	if errors.Is(err, automations.ErrNotFound) {
		h.log.Debug("hooks: unknown or disabled trigger")
		hookError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		h.log.Warn("hooks: trigger lookup failed", "trigger_id", id, "error", err)
		hookError(w, http.StatusInternalServerError, "hook_unavailable")
		return
	}
	var cfg automations.WebhookConfig
	if err := json.Unmarshal(trigger.Config, &cfg); err != nil {
		h.log.Warn("hooks: trigger config unreadable", "trigger_id", id, "error", err)
		hookError(w, http.StatusInternalServerError, "hook_unavailable")
		return
	}
	if !h.limiter.allow(trigger.ID, now) {
		h.log.Warn("hooks: rate limited", "trigger_id", trigger.ID)
		hookError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, hookBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.log.Warn("hooks: body too large", "trigger_id", trigger.ID)
			hookError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
			return
		}
		h.log.Warn("hooks: body read failed", "trigger_id", trigger.ID, "error", err)
		hookError(w, http.StatusBadRequest, "bad_request")
		return
	}
	secret, err := h.resolveSecret(ctx, trigger.CredentialRef)
	if err != nil || secret == "" {
		h.log.Warn("hooks: signing key unavailable", "trigger_id", trigger.ID, "credential_ref", trigger.CredentialRef, "error", err)
		hookError(w, http.StatusInternalServerError, "hook_unavailable")
		return
	}
	if !verifyHook(cfg.Scheme, []byte(secret), r.Header, body, now) {
		h.log.Warn("hooks: signature rejected", "trigger_id", trigger.ID, "scheme", cfg.Scheme)
		h.recordAuthFailure(ctx, trigger, automation, now)
		hookError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !automations.MatchWebhookFilters(cfg.Filters, body) {
		h.log.Debug("hooks: delivery ignored by filters", "trigger_id", trigger.ID)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}
	delivery := hookDelivery(cfg.Scheme, r.Header, body)
	ev, err := events.WebhookReceived(events.WebhookPayload{
		TriggerID: trigger.ID, AutomationID: automation.ID, Scheme: cfg.Scheme, Delivery: delivery,
		ReceivedAt: now.UTC(), Headers: hookHeaders(r.Header), Body: body,
	})
	if err != nil {
		h.log.Warn("hooks: build event failed", "trigger_id", trigger.ID, "error", err)
		hookError(w, http.StatusInternalServerError, "hook_unavailable")
		return
	}
	eventID, inserted, err := h.events.AddIfNew(ctx, ev)
	if err != nil {
		h.log.Warn("hooks: event insert failed", "trigger_id", trigger.ID, "error", err)
		hookError(w, http.StatusInternalServerError, "hook_unavailable")
		return
	}
	if !inserted {
		h.log.Info("hooks: duplicate delivery", "trigger_id", trigger.ID, "delivery", delivery)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "duplicate"})
		return
	}
	if h.kick != nil {
		h.kick()
	}
	if err := h.store.RecordHookDelivery(ctx, trigger.ID, now); err != nil {
		h.log.Warn("hooks: record delivery failed", "trigger_id", trigger.ID, "error", err)
	}
	h.log.Info("hooks: delivery accepted", "trigger_id", trigger.ID, "automation_id", automation.ID, "event_id", eventID, "delivery", delivery)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "event_id": eventID})
}

// recordAuthFailure counts a rejected signature and notifies the
// operator when it disables the trigger.
func (h *hookAPI) recordAuthFailure(ctx context.Context, trigger automations.Trigger, automation automations.Automation, now time.Time) {
	disabled, err := h.store.RecordHookAuthFailure(ctx, trigger.ID, now)
	if err != nil {
		h.log.Warn("hooks: record auth failure failed", "trigger_id", trigger.ID, "error", err)
		return
	}
	if !disabled {
		return
	}
	h.log.Warn("hooks: trigger disabled after repeated signature failures", "trigger_id", trigger.ID, "automation_id", automation.ID)
	if h.notify == nil {
		return
	}
	msg := fmt.Sprintf("%s: webhook trigger disabled after %d failed signatures in %s", automation.Name,
		automations.HookAuthFailureLimit, automations.HookAuthFailureWindow)
	if err := h.notify(ctx, "automation_trigger_disabled", msg); err != nil {
		h.log.Warn("hooks: disable notification failed", "trigger_id", trigger.ID, "error", err)
	}
}

// verifyHook checks a delivery's HMAC-SHA256 signature in constant
// time. github: X-Hub-Signature-256 "sha256=<hex>" over the body.
// generic: X-Timothy-Signature "<hex>" over "<timestamp>.<body>", with
// X-Timothy-Timestamp (unix seconds) within hookTimestampSkew of now.
func verifyHook(scheme string, secret []byte, header http.Header, body []byte, now time.Time) bool {
	mac := hmac.New(sha256.New, secret)
	var sig string
	switch scheme {
	case automations.WebhookSchemeGitHub:
		v, ok := strings.CutPrefix(header.Get("X-Hub-Signature-256"), "sha256=")
		if !ok {
			return false
		}
		sig = v
		mac.Write(body)
	case automations.WebhookSchemeGeneric:
		ts := header.Get("X-Timothy-Timestamp")
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return false
		}
		if skew := now.Sub(time.Unix(sec, 0)); skew > hookTimestampSkew || skew < -hookTimestampSkew {
			return false
		}
		sig = header.Get("X-Timothy-Signature")
		mac.Write([]byte(ts + "."))
		mac.Write(body)
	default:
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) == 0 {
		return false
	}
	return hmac.Equal(got, mac.Sum(nil))
}

// hookDelivery is the delivery id: X-GitHub-Delivery for the github
// scheme, else X-Timothy-Delivery, else the body's sha256.
func hookDelivery(scheme string, header http.Header, body []byte) string {
	var id string
	if scheme == automations.WebhookSchemeGitHub {
		id = header.Get("X-GitHub-Delivery")
	} else {
		id = header.Get("X-Timothy-Delivery")
	}
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// hookHeaders keeps the few headers a goal may reference.
func hookHeaders(header http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"Content-Type", "User-Agent", "X-GitHub-Event"} {
		if v := header.Get(k); v != "" {
			out[strings.ToLower(k)] = v
		}
	}
	return out
}

// hookLimiter is a token bucket per trigger id. In memory is enough:
// one brain process serves /hooks, and a restart only refills buckets.
// Only ids of existing webhook triggers reach it.
type hookLimiter struct {
	mu      sync.Mutex
	buckets map[string]*hookBucket
}

type hookBucket struct {
	tokens float64
	last   time.Time
}

func newHookLimiter() *hookLimiter {
	return &hookLimiter{buckets: map[string]*hookBucket{}}
}

// allow takes one token from id's bucket, refilled at hookBurst per
// hookRefill up to hookBurst.
func (l *hookLimiter) allow(id string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[id]
	if !ok {
		b = &hookBucket{tokens: hookBurst, last: now}
		l.buckets[id] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(hookBurst, b.tokens+elapsed.Seconds()*hookBurst/hookRefill.Seconds())
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
