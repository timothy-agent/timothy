package destinations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeMailSender struct {
	plainCalls int
	lastBody   string
	attached   []MailAttachment
}

func (f *fakeMailSender) SendMail(_ context.Context, _, _, _, body string) error {
	f.plainCalls++
	f.lastBody = body
	return nil
}

func (f *fakeMailSender) SendMailWithAttachments(_ context.Context, _, _, _, body string, attachments []MailAttachment) error {
	f.lastBody = body
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
