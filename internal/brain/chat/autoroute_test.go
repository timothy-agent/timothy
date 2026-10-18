package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
)

// TestChatAutoRouteIntentDeadlineFallsBack confirms a decide call that
// outlives routeIntentTimeout falls back to the default role's route
// without failing the turn (D-164).
func TestChatAutoRouteIntentDeadlineFallsBack(t *testing.T) {
	t.Parallel()
	var (
		mu        sync.Mutex
		remaining time.Duration
		hasDL     bool
	)
	gw := &fakeGW{
		events:     okEvents("hello"),
		decideRole: "decide-llm",
		candidates: []gwclient.RouteCandidate{
			{Name: "default"},
			{Name: "coding", Description: "Software engineering and coding"},
		},
		decideFn: func(ctx context.Context, _ gwclient.DecideRequest) (gwclient.DecideResponse, error) {
			dl, ok := ctx.Deadline()
			mu.Lock()
			remaining, hasDL = time.Until(dl), ok
			mu.Unlock()
			<-ctx.Done()
			return gwclient.DecideResponse{}, ctx.Err()
		},
	}
	s := newService(gw, newFakeLog())

	start := time.Now()
	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "fix this bug"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	elapsed := time.Since(start)
	drain(t, ch)

	mu.Lock()
	defer mu.Unlock()
	if !hasDL || remaining > routeIntentTimeout {
		t.Fatalf("decide deadline: set=%v remaining=%s, want set and <= %s", hasDL, remaining, routeIntentTimeout)
	}
	if elapsed > routeIntentTimeout+2*time.Second {
		t.Fatalf("Chat took %s to start the turn, want about %s", elapsed, routeIntentTimeout)
	}
	if req := gw.lastChatRequest(); req.Route != "default" {
		t.Fatalf("req.Route = %q, want default after decide timeout", req.Route)
	}
}

// TestChatAutoRouteIntentPin covers the session pin: a later Auto turn
// reuses the earlier pick without a decide call until the pinned route
// turns unusable or a turn names a route explicitly (D-164, D-018).
func TestChatAutoRouteIntentPin(t *testing.T) {
	t.Parallel()
	type turn struct {
		route     string // explicit Request.Route; empty is Auto
		unusable  string // route the gateway reports unusable from this turn on
		choice    string // decider answer; empty makes the decide call fail
		wantRoute string
		wantPick  string
	}
	cases := []struct {
		name        string
		turns       []turn
		wantDecides int
	}{
		{
			name: "pinned route reused",
			turns: []turn{
				{choice: "coding", wantRoute: "coding", wantPick: "coding"},
				{choice: "research", wantRoute: "coding", wantPick: "coding"},
				{choice: "research", wantRoute: "coding", wantPick: "coding"},
			},
			wantDecides: 1,
		},
		{
			name: "pin dropped when route unusable",
			turns: []turn{
				{choice: "coding", wantRoute: "coding", wantPick: "coding"},
				{unusable: "coding", choice: "research", wantRoute: "research", wantPick: "research"},
			},
			wantDecides: 2,
		},
		{
			name: "explicit route clears pin",
			turns: []turn{
				{choice: "coding", wantRoute: "coding", wantPick: "coding"},
				{route: "smart", wantRoute: "smart"},
				{choice: "research", wantRoute: "research", wantPick: "research"},
			},
			wantDecides: 2,
		},
		{
			name: "failed pick pins default",
			turns: []turn{
				{choice: "", wantRoute: "default", wantPick: "default"},
				{choice: "coding", wantRoute: "default", wantPick: "default"},
			},
			wantDecides: 1,
		},
		{
			name: "unusable pick pins default",
			turns: []turn{
				{unusable: "coding", choice: "coding", wantRoute: "default", wantPick: "default"},
				{choice: "coding", wantRoute: "default", wantPick: "default"},
			},
			wantDecides: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var (
				mu       sync.Mutex
				choice   string
				unusable string
			)
			gw := &fakeGW{
				events:     okEvents("hello"),
				decideRole: "fast-decider",
				candidates: []gwclient.RouteCandidate{
					{Name: "default"},
					{Name: "coding", Description: "Software engineering and coding"},
					{Name: "research", Description: "Research and sources"},
				},
				decideFn: func(_ context.Context, _ gwclient.DecideRequest) (gwclient.DecideResponse, error) {
					mu.Lock()
					defer mu.Unlock()
					if choice == "" {
						return gwclient.DecideResponse{}, errors.New("decide failed")
					}
					return gwclient.DecideResponse{Choice: choice}, nil
				},
				resolveFn: func(_ context.Context, name, _, _ string) (*gwclient.ResolvedRoute, error) {
					mu.Lock()
					defer mu.Unlock()
					return &gwclient.ResolvedRoute{Route: name, Entries: []gwclient.ResolvedRouteEntry{
						{Model: "m", Usable: name != unusable},
					}}, nil
				},
			}
			log := newFakeLog()
			s := newService(gw, log)
			for i, tt := range tc.turns {
				mu.Lock()
				choice = tt.choice
				if tt.unusable != "" {
					unusable = tt.unusable
				}
				mu.Unlock()
				_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "turn", Route: tt.route})
				if err != nil {
					t.Fatalf("turn %d: Chat: %v", i, err)
				}
				drain(t, ch)
				waitFor(t, func() bool { return !s.TurnActive("s1") })
				if req := gw.lastChatRequest(); req.Route != tt.wantRoute {
					t.Fatalf("turn %d: req.Route = %q, want %q", i, req.Route, tt.wantRoute)
				}
				events, err := log.Events(t.Context(), "s1")
				if err != nil {
					t.Fatalf("turn %d: Events: %v", i, err)
				}
				msg, ok := lastUserMessagePayload(events)
				if !ok {
					t.Fatalf("turn %d: no user_message persisted", i)
				}
				if msg.RoutePick != tt.wantPick {
					t.Fatalf("turn %d: RoutePick = %q, want %q", i, msg.RoutePick, tt.wantPick)
				}
			}
			gw.mu.Lock()
			defer gw.mu.Unlock()
			if len(gw.decideReqs) != tc.wantDecides {
				t.Fatalf("decide calls = %d, want %d", len(gw.decideReqs), tc.wantDecides)
			}
			for _, r := range gw.decideReqs {
				if r.Purpose != "route_intent" || r.SessionID != "s1" {
					t.Fatalf("decide req purpose=%q session=%q, want route_intent and s1", r.Purpose, r.SessionID)
				}
			}
		})
	}
}

// TestChatAutoRouteIntentUnboundIgnoresPin confirms an unbound decide
// role takes the default-role path even when the session holds a pick.
func TestChatAutoRouteIntentUnboundIgnoresPin(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{
		events: okEvents("hello"),
		candidates: []gwclient.RouteCandidate{
			{Name: "default"},
			{Name: "coding", Description: "Software engineering and coding"},
		},
	}
	log := newFakeLog()
	if _, err := log.Append(t.Context(), "s1", session.KindUserMessage, session.UserMessage{Text: "earlier", Route: "coding", RoutePick: "coding"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newService(gw, log)
	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "fix bug"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	if req := gw.lastChatRequest(); req.Route != "default" {
		t.Fatalf("req.Route = %q, want default when decide is unbound", req.Route)
	}
}

// TestResolveAutoRouteBoundsSlowDecideOverHTTP drives the pick through
// the real gateway client against a gateway that never answers, as an
// llm-fallback decider on a slow chat model would: the pick returns
// the default route near routeIntentTimeout and the request is
// cancelled on the wire.
func TestResolveAutoRouteBoundsSlowDecideOverHTTP(t *testing.T) {
	t.Parallel()
	cancelled := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/routes/roles", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"roles":{"default":"default","decide":"decide-llm"}}`)
	})
	mux.HandleFunc("GET /v1/routes/candidates", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"candidates":[{"name":"default"},{"name":"coding","description":"Software engineering"}]}`)
	})
	mux.HandleFunc("GET /v1/routes/{name}/resolve", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"entries":[{"model":"m","usable":true}]}`)
	})
	mux.HandleFunc("POST /v1/decide", func(w http.ResponseWriter, r *http.Request) {
		// The server only notices a client hang-up once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			cancelled <- struct{}{}
		case <-time.After(10 * time.Second):
			_, _ = io.WriteString(w, `{"choice":"coding"}`)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newService(gwclient.New(srv.URL), newFakeLog())
	start := time.Now()
	got := s.resolveAutoRoute(t.Context(), "s1", nil, "fix this bug")
	elapsed := time.Since(start)
	if got != "default" {
		t.Fatalf("resolveAutoRoute = %q, want default after the decide deadline", got)
	}
	if elapsed < routeIntentTimeout || elapsed > routeIntentTimeout+2*time.Second {
		t.Fatalf("resolveAutoRoute took %s, want about %s", elapsed, routeIntentTimeout)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("gateway never saw the decide request cancelled")
	}
}

// TestLastRoutePick covers which user message the pin reads.
func TestLastRoutePick(t *testing.T) {
	t.Parallel()
	um := func(m session.UserMessage) session.Event {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return session.Event{Kind: session.KindUserMessage, Payload: b}
	}
	turn := session.Event{Kind: session.KindAssistantTurn, Payload: []byte(`{}`)}
	cases := []struct {
		name   string
		events []session.Event
		want   string
	}{
		{"no events", nil, ""},
		{"latest message picked", []session.Event{um(session.UserMessage{RoutePick: "coding"}), turn}, "coding"},
		{"latest message explicit", []session.Event{um(session.UserMessage{RoutePick: "coding"}), turn, um(session.UserMessage{Route: "smart"}), turn}, ""},
		{"latest message repicked", []session.Event{um(session.UserMessage{RoutePick: "coding"}), turn, um(session.UserMessage{RoutePick: "research"})}, "research"},
		{"malformed payload", []session.Event{{Kind: session.KindUserMessage, Payload: []byte(`{`)}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lastRoutePick(tc.events); got != tc.want {
				t.Fatalf("lastRoutePick = %q, want %q", got, tc.want)
			}
		})
	}
}

func lastUserMessagePayload(events []session.Event) (session.UserMessage, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != session.KindUserMessage {
			continue
		}
		var msg session.UserMessage
		if err := json.Unmarshal(events[i].Payload, &msg); err != nil {
			return session.UserMessage{}, false
		}
		return msg, true
	}
	return session.UserMessage{}, false
}
