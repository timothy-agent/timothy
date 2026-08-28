package tools

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// MediaRef points at one piece of media a tool emitted during its
// Execute call — an id in the attachment store plus the metadata a UI
// needs to render it, never bytes (D-045).
type MediaRef struct {
	ID   string
	Mime string
	Name string
}

// MaxMediaPerCall caps how many media items one tool call may emit;
// beyond this Emit returns an error instead of silently truncating, so
// the tool's own text feedback can tell the model what happened.
const MaxMediaPerCall = 4

// SaveFunc persists r's bytes and returns the stored id and sniffed
// mime type — attachments.Store.Save's shape, defined locally so tools
// doesn't import the attachments package (would cycle through
// builtin's callers).
type SaveFunc func(ctx context.Context, r io.Reader) (id, mime string, err error)

// Collector accumulates media a single tool call emits. One Collector
// is attached to the context per Execute call by the loop, then
// drained after Execute returns.
type Collector struct {
	saver SaveFunc
	mu    sync.Mutex
	refs  []MediaRef
}

// NewCollector returns a Collector backed by saver. saver nil means
// media emission is unconfigured; Emit then always errors.
func NewCollector(saver SaveFunc) *Collector {
	return &Collector{saver: saver}
}

// Emit saves r's content and records it as one of this call's media
// refs. Beyond MaxMediaPerCall, or after Execute already returned an
// error, this cannot be avoided without either failing the whole tool
// call or the caller checking length after the fact — the error return
// lets the tool decide.
func (c *Collector) Emit(ctx context.Context, name string, r io.Reader) (MediaRef, error) {
	if c == nil || c.saver == nil {
		return MediaRef{}, fmt.Errorf("media emission is not configured")
	}
	c.mu.Lock()
	if len(c.refs) >= MaxMediaPerCall {
		c.mu.Unlock()
		return MediaRef{}, fmt.Errorf("this call already emitted the maximum of %d media items", MaxMediaPerCall)
	}
	c.mu.Unlock()

	id, mime, err := c.saver(ctx, r)
	if err != nil {
		return MediaRef{}, fmt.Errorf("save media: %w", err)
	}
	ref := MediaRef{ID: id, Mime: mime, Name: name}

	c.mu.Lock()
	c.refs = append(c.refs, ref)
	c.mu.Unlock()
	return ref, nil
}

// Drain returns every ref emitted so far and clears the collector.
func (c *Collector) Drain() []MediaRef {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	refs := c.refs
	c.refs = nil
	return refs
}

// collectorCtxKey is the context key a Collector rides on.
type collectorCtxKey struct{}

// WithCollector attaches c to ctx for the duration of one tool Execute
// call.
func WithCollector(ctx context.Context, c *Collector) context.Context {
	return context.WithValue(ctx, collectorCtxKey{}, c)
}

// CollectorFrom returns the Collector attached to ctx, or nil if none
// — a tool built before media emission existed, or a call context the
// loop never attached one to.
func CollectorFrom(ctx context.Context) *Collector {
	c, _ := ctx.Value(collectorCtxKey{}).(*Collector)
	return c
}
