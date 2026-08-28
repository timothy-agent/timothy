package destinations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

type fakeMailSender struct {
	plainCalls    int
	htmlCalls     int
	lastSubject   string
	lastBody      string
	lastPlainText string
	lastHTML      string
	attached      []MailAttachment
}

func (f *fakeMailSender) SendMail(_ context.Context, _, _, subject, body string) error {
	f.plainCalls++
	f.lastSubject = subject
	f.lastBody = body
	return nil
}

func (f *fakeMailSender) SendMailWithAttachments(_ context.Context, _, _, _, body string, attachments []MailAttachment) error {
	f.lastBody = body
	f.attached = attachments
	return nil
}

func (f *fakeMailSender) SendMailHTML(_ context.Context, _, _, subject, plainFallback, htmlBody string, attachments []MailAttachment) error {
	f.htmlCalls++
	f.lastSubject = subject
	f.lastPlainText = plainFallback
	f.lastHTML = htmlBody
	f.attached = attachments
	return nil
}

func TestEmailAdapterDeliverNoFilesUsesPlainSend(t *testing.T) {
	mail := &fakeMailSender{}
	a := &EmailAdapter{Mail: mail}
	err := a.Deliver(t.Context(), json.RawMessage(`{"connector_id":"c1","to":"a@b.com"}`), "", Payload{Body: "digest"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if mail.plainCalls != 1 || mail.attached != nil {
		t.Fatalf("expected a plain send with no attachments, got calls=%d attached=%v", mail.plainCalls, mail.attached)
	}
}

func TestEmailAdapterDeliverWithFilesUsesAttachments(t *testing.T) {
	mail := &fakeMailSender{}
	a := &EmailAdapter{Mail: mail}
	files := []File{{Name: "out.md", Data: []byte("content")}}
	err := a.Deliver(t.Context(), json.RawMessage(`{"connector_id":"c1","to":"a@b.com"}`), "", Payload{Body: "digest", Files: files})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if mail.plainCalls != 0 {
		t.Fatalf("expected the attachment send path, not the plain one")
	}
	if len(mail.attached) != 1 || mail.attached[0].Name != "out.md" || string(mail.attached[0].Data) != "content" {
		t.Fatalf("unexpected attachments: %+v", mail.attached)
	}
}

func TestEmailAdapterUsesMissionSubjectByDefault(t *testing.T) {
	mail := &fakeMailSender{}
	a := &EmailAdapter{Mail: mail}
	err := a.Deliver(t.Context(), json.RawMessage(`{"connector_id":"c1","to":"a@b.com"}`), "", Payload{Name: "Ship it", Body: "digest"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if mail.lastSubject != "Timothy mission: Ship it" {
		t.Fatalf("subject = %q, want mission-derived default", mail.lastSubject)
	}
}

func TestEmailAdapterUsesPayloadSubjectWhenSet(t *testing.T) {
	mail := &fakeMailSender{}
	a := &EmailAdapter{Mail: mail}
	err := a.Deliver(t.Context(), json.RawMessage(`{"connector_id":"c1","to":"a@b.com"}`), "", Payload{Name: "Ship it", Subject: "Daily digest", Body: "digest"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if mail.lastSubject != "Daily digest" {
		t.Fatalf("subject = %q, want the payload's own subject", mail.lastSubject)
	}
}

func TestEmailAdapterDeliverWithTextArtifactsUsesHTML(t *testing.T) {
	mail := &fakeMailSender{}
	a := &EmailAdapter{Mail: mail}
	texts := []TextArtifact{{Name: "digest.md", Content: "# Digest\n\nsomething **important**."}}
	err := a.Deliver(t.Context(), json.RawMessage(`{"connector_id":"c1","to":"a@b.com"}`), "", Payload{Name: "Ship it", Body: "Mission complete: Ship it", TextArtifacts: texts})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if mail.htmlCalls != 1 || mail.plainCalls != 0 {
		t.Fatalf("expected the HTML send path, got htmlCalls=%d plainCalls=%d", mail.htmlCalls, mail.plainCalls)
	}
	if !strings.Contains(mail.lastHTML, "<strong>important</strong>") {
		t.Fatalf("expected rendered HTML content, got %q", mail.lastHTML)
	}
	if !strings.Contains(mail.lastPlainText, "**important**") {
		t.Fatalf("expected the raw markdown in the plain-text fallback, got %q", mail.lastPlainText)
	}
}

func TestWebhookAdapterJSONNeverIncludesFiles(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := &WebhookAdapter{}
	payload := Payload{
		Body:  "digest",
		Files: []File{{Name: "secret.txt", Data: []byte("should never appear in the webhook body")}},
	}
	cfg, _ := json.Marshal(WebhookConfig{URL: srv.URL, Format: "json"})
	if err := a.Deliver(t.Context(), cfg, "", payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if strings.Contains(gotBody, "secret.txt") || strings.Contains(gotBody, "should never appear") {
		t.Fatalf("webhook JSON body leaked file content: %q", gotBody)
	}
}

func TestWebhookAdapterJSONIncludesArtifactRefs(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := &WebhookAdapter{}
	payload := Payload{
		Body:         "digest",
		ArtifactRefs: []missions.ArtifactRef{{ID: "att-1", Mime: "text/markdown", Name: "report.md"}},
	}
	cfg, _ := json.Marshal(WebhookConfig{URL: srv.URL, Format: "json"})
	if err := a.Deliver(t.Context(), cfg, "", payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	for _, want := range []string{"att-1", "text/markdown", "report.md"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("webhook JSON body = %q, want it to include %q", gotBody, want)
		}
	}
}

func TestWebhookAdapterTextMentionsOversizeByNameOnly(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := &WebhookAdapter{}
	payload := Payload{Body: "digest", OversizeFiles: []string{"huge.zip"}}
	cfg, _ := json.Marshal(WebhookConfig{URL: srv.URL, Format: "text"})
	if err := a.Deliver(t.Context(), cfg, "", payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !strings.Contains(gotBody, "huge.zip") {
		t.Fatalf("expected the oversize file name mentioned in text body, got %q", gotBody)
	}
}

func TestWebhookAdapterTextPrependsSubjectWhenSet(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := &WebhookAdapter{}
	payload := Payload{Subject: "Daily digest", Body: "the content"}
	cfg, _ := json.Marshal(WebhookConfig{URL: srv.URL, Format: "text"})
	if err := a.Deliver(t.Context(), cfg, "", payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !strings.HasPrefix(gotBody, "Daily digest\n\nthe content") {
		t.Fatalf("gotBody = %q, want subject prepended", gotBody)
	}
}

func TestWebhookAdapterJSONIncludesSubjectField(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := &WebhookAdapter{}
	payload := Payload{Subject: "Daily digest", Body: "the content"}
	cfg, _ := json.Marshal(WebhookConfig{URL: srv.URL, Format: "json"})
	if err := a.Deliver(t.Context(), cfg, "", payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !strings.Contains(gotBody, `"subject":"Daily digest"`) {
		t.Fatalf("gotBody = %q, want subject field present", gotBody)
	}
}
