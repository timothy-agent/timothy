package channels

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	// maxInputChars caps an inbound message; longer ones are refused.
	maxInputChars = 4000
	// senderRate is the per-sender budget per senderWindow.
	senderRate   = 10
	senderWindow = time.Minute
	// slowDownEvery spaces "Slow down" replies to one sender.
	slowDownEvery = time.Minute
	// queueDepth is the per-conversation backlog behind a running turn.
	queueDepth = 3
	// codeTTL is a pairing code's lifetime; promptEvery spaces pairing
	// prompts to one sender.
	codeTTL     = 10 * time.Minute
	promptEvery = 10 * time.Minute
	// messageLimit is Telegram's text cap in UTF-16 units; streamWindow
	// is the tail shown while a reply streams.
	messageLimit = 4096
	streamWindow = 4000
)

// limiter is a per-key token bucket: senderRate tokens refilled
// evenly over senderWindow.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	warned time.Time
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*bucket{}} }

// allow spends one token for key. When refused, warn is true at most
// once per slowDownEvery.
func (l *limiter) allow(key string, now time.Time) (ok, warn bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, found := l.buckets[key]
	if !found {
		b = &bucket{tokens: senderRate, last: now}
		l.buckets[key] = b
	}
	refill := now.Sub(b.last).Seconds() * senderRate / senderWindow.Seconds()
	b.tokens = min(senderRate, b.tokens+refill)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, false
	}
	if now.Sub(b.warned) >= slowDownEvery {
		b.warned = now
		return false, true
	}
	return false, false
}

// pairingStep is what the pipeline does with a sender's message.
type pairingStep int

const (
	stepContinue pairingStep = iota // approved: go on to the model
	stepDrop                        // revoked: no reply, no model
	stepRedeem                      // pending, text is the live code
	stepPrompt                      // pending, send a new code prompt
	stepSilent                      // pending, prompted recently
)

// decidePairing is the pairing state machine. No step but
// stepContinue reaches a model.
func decidePairing(p Pairing, text string, now time.Time) pairingStep {
	switch p.Status {
	case StatusApproved:
		return stepContinue
	case StatusPending:
	default:
		return stepDrop
	}
	code := strings.TrimSpace(text)
	if looksLikeCode(code) && p.Code != "" && p.CodeExpiresAt != nil && now.Before(*p.CodeExpiresAt) &&
		subtle.ConstantTimeCompare([]byte(code), []byte(p.Code)) == 1 {
		return stepRedeem
	}
	if p.LastPromptAt == nil || now.Sub(*p.LastPromptAt) >= promptEvery {
		return stepPrompt
	}
	return stepSilent
}

func looksLikeCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// newCode returns a uniformly random 6-digit code.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// tgLen is s's length in UTF-16 units, as Telegram counts.
func tgLen(s string) int { return len(utf16.Encode([]rune(s))) }

// streamingView is the text shown while a reply streams: the whole
// text, or "..." plus its last streamWindow units.
func streamingView(text string) string {
	if tgLen(text) <= streamWindow {
		return text
	}
	runes := []rune(text)
	n := 0
	i := len(runes)
	for i > 0 {
		w := len(utf16.Encode(runes[i-1 : i]))
		if n+w > streamWindow {
			break
		}
		n += w
		i--
	}
	return "..." + string(runes[i:])
}

// chunkReply splits text into pieces of at most limit UTF-16 units,
// preferring paragraph breaks, then line breaks, then spaces.
func chunkReply(text string, limit int) []string {
	var out []string
	for tgLen(text) > limit {
		runes := []rune(text)
		cut, n := 0, 0
		for cut < len(runes) {
			w := len(utf16.Encode(runes[cut : cut+1]))
			if n+w > limit {
				break
			}
			n += w
			cut++
		}
		head := string(runes[:cut])
		split := len(head)
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(head, sep); i > 0 {
				split = i
				break
			}
		}
		if piece := strings.TrimRight(head[:split], " \n"); piece != "" {
			out = append(out, piece)
		}
		text = strings.TrimLeft(text[split:], " \n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// editThrottle decides when a streaming edit is due: on a tick, and
// only when the view changed since the last edit.
type editThrottle struct {
	sent string
}

// due returns the view to send for text, or false when unchanged.
func (t *editThrottle) due(text string) (string, bool) {
	view := streamingView(text)
	if view == t.sent || strings.TrimSpace(view) == "" {
		return "", false
	}
	t.sent = view
	return view, true
}
