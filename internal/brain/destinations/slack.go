package destinations

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
)

// slackMessageLimit keeps one Slack message inside a section's 3000
// characters.
const slackMessageLimit = 3000

// slackSender sends a channel destination's payload via the Slack Web
// API, in the same priority order as telegramSender: text artifacts as
// mrkdwn messages, else files with the title on the first, else the
// completion line.
type slackSender struct{ client *channels.SlackClient }

func (s slackSender) deliver(ctx context.Context, channelID, threadTS string, p Payload) error {
	head := slackHeader(p)
	switch {
	case len(p.TextArtifacts) > 0:
		var parts []string
		for i, ta := range p.TextArtifacts {
			if i > 0 {
				parts = append(parts, sendTextArtifactSeparator)
			}
			if len(p.TextArtifacts) > 1 {
				parts = append(parts, "## "+ta.Name+"\n\n")
			}
			parts = append(parts, ta.Content)
		}
		return s.post(ctx, channelID, threadTS, joinHead(head, SlackMrkdwn(strings.Join(parts, ""))))
	case len(p.Files) > 0:
		for i, f := range p.Files {
			comment := ""
			if i == 0 {
				comment = head
			}
			if err := s.client.UploadFile(ctx, channelID, threadTS, f.Name, f.Data, comment); err != nil {
				return classifySendErr(err)
			}
		}
		return nil
	default:
		body := SlackMrkdwn(p.Body)
		if len(p.Links) > 0 {
			body += "\n\nLinks:"
			for _, l := range p.Links {
				body += "\n- " + slackEscape(l)
			}
		}
		body += slackEscape(oversizeNotice(p.OversizeFiles))
		if head == "" && p.Subject != "" {
			head = "*" + slackEscape(p.Subject) + "*"
		}
		return s.post(ctx, channelID, threadTS, joinHead(head, body))
	}
}

func (s slackSender) post(ctx context.Context, channelID, threadTS, text string) error {
	for _, chunk := range chunkText(text, slackMessageLimit) {
		body := map[string]any{"channel": channelID, "text": chunk, "mrkdwn": true}
		if threadTS != "" {
			body["thread_ts"] = threadTS
		}
		if err := s.client.Call(ctx, "chat.postMessage", body, nil); err != nil {
			return classifySendErr(err)
		}
	}
	return nil
}

// slackHeader is the bold title and completion date, "" without a
// name.
func slackHeader(p Payload) string {
	if p.Name == "" {
		return ""
	}
	h := "*" + slackEscape(p.Name) + "*"
	if !p.CompletedAt.IsZero() {
		h += "\n" + p.CompletedAt.Format("2 Jan 2006, 15:04 MST")
	}
	return h
}

func joinHead(head, body string) string {
	if head == "" {
		return body
	}
	return head + "\n\n" + body
}

// slackEscape escapes the characters Slack parses in text, so content
// cannot mention users or channels.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

var (
	mdHeading = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	mdBold    = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

// SlackMrkdwn converts Markdown to Slack mrkdwn: headings and **bold**
// become *bold*, [text](url) becomes <url|text>, code fences and inline
// code stay as they are. Everything is escaped first.
func SlackMrkdwn(md string) string {
	lines := strings.Split(slackEscape(md), "\n")
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if m := mdHeading.FindStringSubmatch(l); m != nil {
			lines[i] = "*" + convertInline(mdBold.ReplaceAllString(m[1], "$1$2")) + "*"
			continue
		}
		lines[i] = convertInline(l)
	}
	return strings.Join(lines, "\n")
}

// convertInline converts bold and links outside inline code spans.
func convertInline(l string) string {
	parts := strings.Split(l, "`")
	for i := 0; i < len(parts); i += 2 {
		p := mdBold.ReplaceAllStringFunc(parts[i], func(m string) string {
			sub := mdBold.FindStringSubmatch(m)
			return "*" + sub[1] + sub[2] + "*"
		})
		parts[i] = mdLink.ReplaceAllString(p, "<$2|$1>")
	}
	return strings.Join(parts, "`")
}

// chunkText splits text into pieces of at most limit runes, preferring
// paragraph breaks, then line breaks, then spaces.
func chunkText(text string, limit int) []string {
	var out []string
	for utf8.RuneCountInString(text) > limit {
		head := string([]rune(text)[:limit])
		split := len(head)
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(head, sep); i > 0 {
				split = i
				break
			}
		}
		if piece := strings.TrimRight(text[:split], " \n"); piece != "" {
			out = append(out, piece)
		}
		text = strings.TrimLeft(text[split:], " \n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}
