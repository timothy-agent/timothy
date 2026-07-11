package provider

import (
	"strings"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Nova models emit chain-of-thought as literal <thinking>…</thinking>
// spans inside the text stream — Converse has no separate reasoning
// channel for them. thinkTagSplitter rewrites those spans into
// reasoning events so the UI treats them like every other model's
// hidden reasoning instead of showing them as answer text. Tags can
// straddle chunk boundaries, so a small carry buffer holds any suffix
// that could still become a tag.
type thinkTagSplitter struct {
	carry    string
	thinking bool
}

const (
	thinkOpen  = "<thinking>"
	thinkClose = "</thinking>"
)

// Feed consumes one text delta and returns the events it resolves to.
func (s *thinkTagSplitter) Feed(text string) []stream.StreamEvent {
	s.carry += text
	var out []stream.StreamEvent
	for {
		tag := thinkOpen
		if s.thinking {
			tag = thinkClose
		}
		if i := strings.Index(s.carry, tag); i >= 0 {
			if seg := s.carry[:i]; seg != "" {
				out = append(out, s.event(seg))
			}
			s.carry = s.carry[i+len(tag):]
			s.thinking = !s.thinking
			continue
		}
		// No full tag: emit everything except a tail that could still
		// grow into one on the next delta.
		keep := partialTagSuffix(s.carry, tag)
		if seg := s.carry[:len(s.carry)-keep]; seg != "" {
			out = append(out, s.event(seg))
		}
		s.carry = s.carry[len(s.carry)-keep:]
		return out
	}
}

// Flush drains whatever the stream ended on, including a dangling
// partial tag — at end-of-stream it can no longer complete.
func (s *thinkTagSplitter) Flush() []stream.StreamEvent {
	if s.carry == "" {
		return nil
	}
	ev := s.event(s.carry)
	s.carry = ""
	return []stream.StreamEvent{ev}
}

func (s *thinkTagSplitter) event(text string) stream.StreamEvent {
	if s.thinking {
		return stream.StreamEvent{Type: stream.EventReasoningChunk, Text: text}
	}
	return stream.StreamEvent{Type: stream.EventChunk, Text: text}
}

// partialTagSuffix reports how many trailing bytes of s are a proper
// prefix of tag (and might complete on the next chunk).
func partialTagSuffix(s, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
