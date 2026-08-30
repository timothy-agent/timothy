// Package pgpool wraps pgxpool with connect-retry so services start
// (and stay up) without a reachable database, reporting degraded
// health instead of crash-looping.
package pgpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDegraded is returned by Get while the database is unreachable.
var ErrDegraded = errors.New("pgpool: database unavailable")

const (
	connectTimeout = 5 * time.Second
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
	pingInterval   = 10 * time.Second
	pingTimeout    = 3 * time.Second
	waitPoll       = 500 * time.Millisecond
)

// Pool manages a pgx connection pool that may not exist yet. A
// background goroutine (owned by the ctx passed to New) connects with
// exponential backoff, watches the connection, and reconnects after
// outages.
type Pool struct {
	dsn string
	log *slog.Logger

	mu      sync.RWMutex
	pool    *pgxpool.Pool
	lastErr error
}

// New starts managing a pool for dsn. It returns immediately; an empty
// dsn leaves the pool permanently degraded until restart. Cancel ctx
// to stop the manager and close the pool.
func New(ctx context.Context, dsn string, log *slog.Logger) *Pool {
	p := &Pool{dsn: dsn, log: log}
	if dsn == "" {
		p.setState(nil, errors.New("DATABASE_URL not set"))
		return p
	}
	p.setState(nil, errors.New("connecting"))
	go p.manage(ctx)
	return p
}

func (p *Pool) manage(ctx context.Context) {
	backoff := initialBackoff
	for ctx.Err() == nil {
		pool, err := p.connect(ctx)
		if err != nil {
			p.setState(nil, err)
			p.log.Warn("database connect failed", "error", err, "retry_in", backoff.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		backoff = initialBackoff
		p.setState(pool, nil)
		p.log.Info("database connected")
		p.watch(ctx, pool)
	}
}

func (p *Pool) connect(ctx context.Context) (*pgxpool.Pool, error) {
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(p.dsn)
	if err != nil {
		return nil, err
	}
	// statement_timeout bounds a single runaway query; every store's
	// transactions in this codebase are Postgres-only (never span a
	// network call), so idle_in_transaction_session_timeout is also
	// safe: it only ever fires on a connection genuinely stuck idle
	// mid-transaction, not one waiting on an LLM/HTTP call.
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "300000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"

	pool, err := pgxpool.NewWithConfig(cctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(cctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// watch pings until the connection is lost or ctx ends, then clears
// state and closes the pool.
func (p *Pool) watch(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.setState(nil, ctx.Err())
			pool.Close()
			return
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := pool.Ping(pctx)
			cancel()
			if err != nil {
				p.setState(nil, fmt.Errorf("ping failed: %w", err))
				pool.Close()
				p.log.Warn("database connection lost", "error", err)
				return
			}
		}
	}
}

func (p *Pool) setState(pool *pgxpool.Pool, err error) {
	p.mu.Lock()
	p.pool, p.lastErr = pool, err
	p.mu.Unlock()
}

// Get returns the live pool or ErrDegraded (wrapped with detail).
func (p *Pool) Get() (*pgxpool.Pool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.pool == nil {
		return nil, fmt.Errorf("%w: %v", ErrDegraded, p.lastErr)
	}
	return p.pool, nil
}

// Healthy reports whether the database is currently reachable.
func (p *Pool) Healthy() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pool != nil
}

// Status returns "ok" or "degraded" plus human-readable detail for
// health endpoints.
func (p *Pool) Status() (status, detail string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.pool != nil {
		return "ok", ""
	}
	if p.lastErr != nil {
		return "degraded", p.lastErr.Error()
	}
	return "degraded", "not connected"
}

// WaitHealthy blocks until the pool is healthy or ctx ends.
func (p *Pool) WaitHealthy(ctx context.Context) error {
	ticker := time.NewTicker(waitPoll)
	defer ticker.Stop()
	for {
		if p.Healthy() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
