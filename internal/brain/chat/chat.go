// Package chat is brain's pass-through chat: assemble a prompt from
// the in-memory session buffer, stream through the gateway, remember
// the exchange. Event-sourced persistence replaces the buffer in the
// next slice; the buffer is DELIBERATELY temporary.
package chat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

const (
	// defaultCategory serves plain chat turns when the caller picks
	// nothing; the web UI exposes a per-message picker.
	defaultCategory = "coding"
	// maxBufferedMessages bounds the temporary per-session history.
	maxBufferedMessages = 20
)

// Gateway is what chat needs from the gateway client.
type Gateway interface {
	Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error)
}

// Service holds the temporary in-memory conversation buffers.
type Service struct {
	gw  Gateway
	db  *pgpool.Pool
	log *slog.Logger

	mu      sync.Mutex
	buffers map[string][]provider.Message
}

func New(gw Gateway, db *pgpool.Pool, log *slog.Logger) *Service {
	log.Info("chat service ready", "system_prompt_version", systemPromptVersion)
	return &Service{gw: gw, db: db, log: log, buffers: map[string][]provider.Message{}}
}

// Request is one chat turn.
type Request struct {
	SessionID    string `json:"session_id,omitempty"`
	Message      string `json:"message"`
	TaskCategory string `json:"task_category,omitempty"`
	ModelHint    string `json:"model_hint,omitempty"`
}

// Chat streams one turn. A missing session id creates a session row
// and the returned id identifies it. The returned channel follows the
// stream package's terminal contract.
func (s *Service) Chat(ctx context.Context, req Request) (string, <-chan stream.StreamEvent, error) {
	if strings.TrimSpace(req.Message) == "" {
		return "", nil, fmt.Errorf("chat: message is required")
	}
	category := req.TaskCategory
	if category == "" {
		category = defaultCategory
	}

	sessionID := req.SessionID
	if sessionID == "" {
		id, err := s.createSession(ctx)
		if err != nil {
			return "", nil, err
		}
		sessionID = id
	}

	userMsg := provider.Message{Role: "user", Content: req.Message}
	messages := append(s.history(sessionID), userMsg)

	upstream, err := s.gw.Stream(ctx, gwclient.StreamRequest{
		TaskCategory: category,
		ModelHint:    req.ModelHint,
		System:       systemPrompt,
		Messages:     messages,
		SessionID:    sessionID,
	})
	if err != nil {
		// Return the id with the error: the session row exists, and the
		// client must reuse it on retry instead of orphaning it.
		return sessionID, nil, err
	}

	out := make(chan stream.StreamEvent)
	go func() {
		defer close(out)
		var reply strings.Builder
		for ev := range upstream {
			if ev.Type == stream.EventChunk {
				reply.WriteString(ev.Text)
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
		if reply.Len() > 0 {
			s.remember(sessionID, userMsg, provider.Message{Role: "assistant", Content: reply.String()})
		}
	}()
	return sessionID, out, nil
}

func (s *Service) createSession(ctx context.Context) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("chat: create session: %w", err)
	}
	var id string
	if err := db.QueryRow(ctx, "INSERT INTO sessions DEFAULT VALUES RETURNING id").Scan(&id); err != nil {
		return "", fmt.Errorf("chat: create session: %w", err)
	}
	return id, nil
}

func (s *Service) history(sessionID string) []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.buffers[sessionID]
	out := make([]provider.Message, len(buf), len(buf)+1)
	copy(out, buf)
	return out
}

func (s *Service) remember(sessionID string, exchange ...provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := append(s.buffers[sessionID], exchange...)
	if len(buf) > maxBufferedMessages {
		buf = buf[len(buf)-maxBufferedMessages:]
	}
	s.buffers[sessionID] = buf
}
