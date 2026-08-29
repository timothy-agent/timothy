package connectors

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// IMAPConfig is the connectors.config shape for kind='imap'. The
// password lives in the connector's credential_ref (secret store),
// never here. Port defaults to 993 (implicit TLS); port 143 selects
// STARTTLS instead. AccountEmail defaults to Username when unset.
// SMTPHost is optional and gates whether send_mail is served at all;
// SMTPPort defaults to 587 (STARTTLS), port 465 selects implicit TLS.
type IMAPConfig struct {
	Host         string `json:"host"`
	Port         int    `json:"port,omitempty"`
	Username     string `json:"username"`
	AccountEmail string `json:"account_email,omitempty"`
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
}

const (
	imapDefaultPort     = 993
	imapSTARTTLSPort    = 143
	imapDefaultSMTPPort = 587
	imapImplicitTLSSMTP = 465
	imapDialTimeout     = 30 * time.Second
	// imapIOTimeout bounds a whole IMAP session (dial through logout),
	// set as an absolute deadline on the raw conn so every command the
	// session issues is covered, not just the initial connect.
	imapIOTimeout = 60 * time.Second
)

// email returns the connected account's email: AccountEmail when set,
// else Username (a bare username is often the email itself for IMAP
// providers).
func (c IMAPConfig) email() string {
	if c.AccountEmail != "" {
		return c.AccountEmail
	}
	return c.Username
}

func (c IMAPConfig) port() int {
	if c.Port != 0 {
		return c.Port
	}
	return imapDefaultPort
}

func (c IMAPConfig) smtpPort() int {
	if c.SMTPPort != 0 {
		return c.SMTPPort
	}
	return imapDefaultSMTPPort
}

// imapConfig decodes connectors.config for kind='imap'.
func imapConfig(c Connector) (IMAPConfig, error) {
	var cfg IMAPConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("imap %s: config: %w", c.Name, err)
	}
	if cfg.Host == "" || cfg.Username == "" {
		return cfg, fmt.Errorf("imap %s: config.host and config.username are required", c.Name)
	}
	return cfg, nil
}

// IMAPBuilder returns the Builder for kind='imap'. client is used for
// markitdown's HTTP call (attachment conversion); a nil client
// defaults inside markitdown.Convert. markItDownURL is the markitdown
// sidecar's base address; empty disables read_mail_attachment with a
// clear per-call error, same convention as Microsoft.MarkItDownURL.
func IMAPBuilder(client *http.Client, markItDownURL string) Builder {
	return func(_ context.Context, c Connector, resolve Resolve) (Source, error) {
		cfg, err := imapConfig(c)
		if err != nil {
			return nil, err
		}
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("imap %s: credential_ref is required (the mailbox password)", c.Name)
		}
		src := &imapSource{
			name:          c.Name,
			cfg:           cfg,
			credentialRef: c.CredentialRef,
			resolve:       resolve,
			client:        client,
			markItDownURL: markItDownURL,
		}
		src.dial = src.realDial
		src.send = realSMTPSend
		return src, nil
	}
}

// imapSource is a built imap-kind connector: password-auth IMAP (and
// optional SMTP) against one mailbox. No connection pooling, dial
// opens a fresh session per tool call, always closed at the end of
// that call.
type imapSource struct {
	name          string
	cfg           IMAPConfig
	credentialRef string
	resolve       Resolve
	client        *http.Client
	markItDownURL string

	// dial and send are overridable seams: IMAPBuilder wires them to
	// realDial/realSMTPSend in production, tests substitute fakes so no
	// real network is touched.
	dial func(ctx context.Context) (imapSession, error)
	send func(ctx context.Context, cfg IMAPConfig, password string, recipients []string, message []byte) error
}

// Tools returns search_mail/read_mail/read_mail_attachment always,
// plus send_mail only when SMTPHost is configured.
func (s *imapSource) Tools() []*tools.Tool {
	out := []*tools.Tool{s.mailSearch(), s.mailRead(), s.mailReadAttachment()}
	if s.cfg.SMTPHost != "" {
		out = append(out, s.mailSend())
	}
	return out
}

// Test dials, logs in, and logs out, the cheapest honest signal the
// stored password still authenticates.
func (s *imapSource) Test(ctx context.Context) error {
	sess, err := s.dial(ctx)
	if err != nil {
		return err
	}
	return sess.Close()
}

func (s *imapSource) Close() error { return nil }

// AccountInfo reports this source's kind and connected account email
// (the accountInfo capability, see manager.go's aggregation).
func (s *imapSource) AccountInfo() (kind, email string) {
	return "imap", s.cfg.email()
}

// Identity dials to prove the credential works and reports it: Login
// is the username, Email the account email, Scopes "imap" or
// "imap+smtp" depending on whether SMTP sending is configured. Reuses
// GitHubIdentity's shape rather than adding a parallel one.
func (s *imapSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	if err := s.Test(ctx); err != nil {
		return GitHubIdentity{}, err
	}
	scopes := "imap"
	if s.cfg.SMTPHost != "" {
		scopes = "imap+smtp"
	}
	return GitHubIdentity{Login: s.cfg.Username, Email: s.cfg.email(), Scopes: scopes}, nil
}

// password resolves the connector's credential_ref.
func (s *imapSource) password(ctx context.Context) (string, error) {
	pw, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return "", fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return pw, nil
}

// realDial opens a real IMAP connection, authenticates, and SELECTs
// INBOX read-only: implicit TLS on the configured port (default 993),
// or STARTTLS when the port is 143. The raw conn is dialed with ctx and
// given an absolute imapIOTimeout deadline (shortened to ctx's deadline
// if that comes sooner) covering the whole session, dial through
// logout, not just the connect: imapclient's own Dialer.Timeout only
// bounds the initial TCP handshake, so a server that accepts the
// connection then stalls mid-command would otherwise hang forever.
// Never pooled, callers close it after one operation.
func (s *imapSource) realDial(ctx context.Context) (imapSession, error) {
	pw, err := s.password(ctx)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.port())
	dialer := &net.Dialer{Timeout: imapDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("imap dial %s: %w", addr, err)
	}
	deadline := time.Now().Add(imapIOTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("imap set deadline: %w", err)
	}

	var client *imapclient.Client
	if s.cfg.port() == imapSTARTTLSPort {
		client, err = imapclient.NewStartTLS(conn, nil)
	} else {
		client = imapclient.New(tls.Client(conn, &tls.Config{ServerName: s.cfg.Host}), nil)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("imap dial %s: %w", addr, err)
	}
	if err := client.Login(s.cfg.Username, pw).Wait(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("imap login: %w", err)
	}
	if _, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("imap select INBOX: %w", err)
	}
	return &imapConn{client: client}, nil
}

// realSMTPSend sends a fully-built RFC822 message via SMTP to
// recipients (To and Cc addresses, the SMTP envelope's RCPT TO list):
// implicit TLS on port 465, STARTTLS otherwise (587 and any other
// port), AUTH PLAIN with the connector's username/password.
func realSMTPSend(ctx context.Context, cfg IMAPConfig, password string, recipients []string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.smtpPort())
	dialer := &net.Dialer{Timeout: imapDialTimeout}

	var conn net.Conn
	var err error
	if cfg.smtpPort() == imapImplicitTLSSMTP {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: cfg.SMTPHost})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(imapIOTimeout))

	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer func() { _ = client.Close() }()

	if cfg.smtpPort() != imapImplicitTLSSMTP {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp server %s does not offer STARTTLS; refusing to authenticate over plaintext", cfg.SMTPHost)
		}
		if err := client.StartTLS(&tls.Config{ServerName: cfg.SMTPHost}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	auth := smtp.PlainAuth("", cfg.Username, password, cfg.SMTPHost)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(cfg.email()); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt to %s: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return client.Quit()
}
