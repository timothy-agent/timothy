package channels

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
)

const fakeMailbox = "timothy@example.com"

// sentMail is one reply the fake mailbox sent.
type sentMail struct {
	ID, To, Subject, Body, InReplyTo string
	References                       []string
}

// fakeMail is an in-memory mailbox: no network. Tests add mail and
// read what was sent.
type fakeMail struct {
	mu     sync.Mutex
	latest uint32
	msgs   []connectors.MailMessage
	files  map[string][]byte
	sent   []sentMail
	newer  int
	n      int
}

func newFakeMail(latest uint32) *fakeMail {
	return &fakeMail{latest: latest, files: map[string][]byte{}}
}

// add appends a message; its UID is one above the highest so far.
func (f *fakeMail) add(m connectors.MailMessage) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest++
	m.UID = f.latest
	f.msgs = append(f.msgs, m)
	return m.UID
}

func (f *fakeMail) sentMails() []sentMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func (f *fakeMail) newerCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newer
}

func (f *fakeMail) box() mailbox {
	return mailbox{
		Address: fakeMailbox,
		LatestUID: func(context.Context) (uint32, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.latest, nil
		},
		Newer: func(_ context.Context, since uint32, max int) ([]connectors.MailMessage, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.newer++
			var out []connectors.MailMessage
			for _, m := range f.msgs {
				if m.UID > since && len(out) < max {
					out = append(out, m)
				}
			}
			return out, nil
		},
		Attachment: func(_ context.Context, uid uint32, name string) ([]byte, string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			raw, ok := f.files[fmt.Sprintf("%d/%s", uid, name)]
			if !ok {
				return nil, "", fmt.Errorf("no attachment %q", name)
			}
			return raw, "application/octet-stream", nil
		},
		Reply: func(_ context.Context, to, subject, body, inReplyTo string, refs []string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.n++
			id := fmt.Sprintf("out%d.timothy@example.com", f.n)
			f.sent = append(f.sent, sentMail{ID: id, To: to, Subject: subject, Body: body, InReplyTo: inReplyTo, References: slices.Clone(refs)})
			return id, nil
		},
	}
}

func (f *fakeMail) open(context.Context, string) (mailbox, error) { return f.box(), nil }
