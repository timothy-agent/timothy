package provider

import (
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// run feeds the chunks and returns the concatenated reasoning and
// answer text the splitter produced.
func runSplitter(t *testing.T, chunks []string) (reasoning, answer string) {
	t.Helper()
	s := &thinkTagSplitter{}
	collect := func(evs []stream.StreamEvent) {
		for _, ev := range evs {
			switch ev.Type {
			case stream.EventReasoningChunk:
				reasoning += ev.Text
			case stream.EventChunk:
				answer += ev.Text
			default:
				t.Fatalf("unexpected event type %v", ev.Type)
			}
		}
	}
	for _, c := range chunks {
		collect(s.Feed(c))
	}
	collect(s.Flush())
	return reasoning, answer
}

func TestThinkTagSplitter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		chunks        []string
		wantReasoning string
		wantAnswer    string
	}{
		{
			name:       "no tags pass through",
			chunks:     []string{"hello ", "world"},
			wantAnswer: "hello world",
		},
		{
			name:          "single span one chunk",
			chunks:        []string{"<thinking>hmm</thinking>answer"},
			wantReasoning: "hmm",
			wantAnswer:    "answer",
		},
		{
			name:          "tags straddle chunk boundaries",
			chunks:        []string{"<thin", "king>deep ", "thought</think", "ing>the answer"},
			wantReasoning: "deep thought",
			wantAnswer:    "the answer",
		},
		{
			name:          "multiple spans",
			chunks:        []string{"<thinking>a</thinking>one<thinking>b</thinking>two"},
			wantReasoning: "ab",
			wantAnswer:    "onetwo",
		},
		{
			name:          "unterminated thinking flushes as reasoning",
			chunks:        []string{"<thinking>never closed"},
			wantReasoning: "never closed",
		},
		{
			name:       "dangling partial tag flushes as text",
			chunks:     []string{"answer <thin"},
			wantAnswer: "answer <thin",
		},
		{
			name:       "angle brackets that are not tags",
			chunks:     []string{"a < b and <thing> stays"},
			wantAnswer: "a < b and <thing> stays",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reasoning, answer := runSplitter(t, tc.chunks)
			if reasoning != tc.wantReasoning || answer != tc.wantAnswer {
				t.Fatalf("reasoning=%q answer=%q, want %q / %q",
					reasoning, answer, tc.wantReasoning, tc.wantAnswer)
			}
		})
	}
}

func TestThinkTagSplitterByteAtATime(t *testing.T) {
	t.Parallel()
	// Worst-case chunking: every byte its own delta.
	full := "<thinking>plan the reply</thinking>Here you go.<thinking>x</thinking>!"
	reasoning, answer := runSplitter(t, strings.Split(full, ""))
	if reasoning != "plan the replyx" || answer != "Here you go.!" {
		t.Fatalf("reasoning=%q answer=%q", reasoning, answer)
	}
}
