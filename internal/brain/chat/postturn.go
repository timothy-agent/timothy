package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/session"
)

// D-175: a chat turn's slot frees as soon as its assistant_turn is
// durable. Distillation, the turn_memory append, long-term memory
// extraction, compaction and auto-title run afterwards as one post-turn
// job on a per-session FIFO queue, so a follow-up never waits on
// side-call LLM latency and two jobs for one session never overlap.
//
// Safety against the next turn, which may start while a job runs:
//   - turn_memory projects at its log position (session.LLMContext). A
//     late one lands after the next turn's user_message; earlier
//     messages never move, so the projection stays prefix-stable and a
//     turn that already projected keeps its own snapshot.
//   - postCompact skips compaction whenever a turn is active on the
//     session (the active turn runs pre-send compaction and queues its
//     own post-turn pass when it ends). When postCompact starts while
//     no turn is active, it holds compactLocks[sessionID], and Chat
//     waits on that lock after turnBegin and before appending its
//     user_message (Retry waits in runTurn before reading Events). So
//     a background compaction never reads a live turn's user_message or
//     pending_state and never races a turn projecting context.

// postTurnQueue runs post-turn jobs one at a time per session, in
// enqueue order, on a context Shutdown cancels.
type postTurnQueue struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	// queues holds each session's pending jobs; a present key means a
	// worker goroutine is draining that session.
	queues map[string][]func(context.Context)
	wg     sync.WaitGroup
}

// enqueue schedules job after every job already queued for sessionID.
// False means the queue is shut down and job was dropped.
func (q *postTurnQueue) enqueue(sessionID string, job func(context.Context)) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if q.queues == nil {
		q.queues = map[string][]func(context.Context){}
		q.ctx, q.cancel = context.WithCancel(context.Background())
	}
	jobs, running := q.queues[sessionID]
	q.queues[sessionID] = append(jobs, job)
	if !running {
		q.wg.Add(1)
		go q.run(sessionID)
	}
	return true
}

func (q *postTurnQueue) run(sessionID string) {
	defer q.wg.Done()
	for {
		q.mu.Lock()
		jobs := q.queues[sessionID]
		if len(jobs) == 0 || q.ctx.Err() != nil {
			delete(q.queues, sessionID)
			q.mu.Unlock()
			return
		}
		job := jobs[0]
		q.queues[sessionID] = jobs[1:]
		ctx := q.ctx
		q.mu.Unlock()
		job(ctx)
	}
}

// shutdown refuses new jobs and waits for queued ones until ctx ends,
// then cancels the rest: queued jobs are dropped and running ones see
// their context cancelled.
func (q *postTurnQueue) shutdown(ctx context.Context) {
	q.mu.Lock()
	q.closed = true
	cancel := q.cancel
	q.mu.Unlock()
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if cancel != nil {
		cancel()
	}
}

// Shutdown stops accepting post-turn work and drains what is queued
// until ctx ends, then cancels whatever is still running. Turns that
// finish afterwards keep their assistant_turn; only the side work is
// skipped, and every piece of it is retried or optional (title keys on
// an empty title, compaction runs again pre-send).
func (s *Service) Shutdown(ctx context.Context) {
	s.post.shutdown(ctx)
}

// keyedLock is a per-key mutex whose Lock honours a context. Entries
// are reference counted so idle sessions leave nothing behind.
type keyedLock struct {
	mu    sync.Mutex
	locks map[string]*keyedEntry
}

type keyedEntry struct {
	sem  chan struct{}
	refs int
}

func (k *keyedLock) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedEntry{}
	}
	e := k.locks[key]
	if e == nil {
		e = &keyedEntry{sem: make(chan struct{}, 1)}
		k.locks[key] = e
	}
	e.refs++
	k.mu.Unlock()

	select {
	case e.sem <- struct{}{}:
		return func() {
			<-e.sem
			k.release(key, e)
		}, nil
	case <-ctx.Done():
		k.release(key, e)
		return nil, ctx.Err()
	}
}

func (k *keyedLock) release(key string, e *keyedEntry) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(k.locks, key)
	}
}

// compact runs one MaybeCompact pass while holding sessionID's
// compaction lock (D-175). Waiting for the lock spends ctx's budget.
func (s *Service) compact(ctx context.Context, sessionID string) error {
	unlock, err := s.compactLocks.lock(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("chat: wait for compaction lock: %w", err)
	}
	defer unlock()
	return s.compactor.MaybeCompact(ctx, sessionID)
}

// waitCompact waits for any in-flight post-turn compaction on sessionID
// to finish before Chat appends a new user_message (D-175).
func (s *Service) waitCompact(ctx context.Context, sessionID string) error {
	unlock, err := s.compactLocks.lock(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("chat: wait for compaction lock: %w", err)
	}
	unlock()
	return nil
}

// postCompact runs background compaction only while no turn is active
// on sessionID (D-175). An active turn handles its own pre-send pass
// and queues a fresh post-turn pass when it finishes.
func (s *Service) postCompact(ctx context.Context, sessionID string) error {
	if s.TurnActive(sessionID) {
		return nil
	}
	unlock, err := s.compactLocks.lock(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("chat: wait for compaction lock: %w", err)
	}
	defer unlock()
	if s.TurnActive(sessionID) {
		return nil
	}
	return s.compactor.MaybeCompact(ctx, sessionID)
}

// postTurn is one completed turn's background work (D-175), run by
// s.post on a context Shutdown cancels. Every stage keeps its own
// budget, as it had when this ran on the turn's clock.
func (s *Service) postTurn(ctx context.Context, sessionID, userText string, profile agents.Agent, needsTitle bool, text string, turnSeq int64, sensitiveRoute string, memoryContents []string) {
	var tm *session.TurnMemory
	if s.distill != nil && text != "" {
		dctx, dcancel := context.WithTimeout(ctx, distillBudget)
		tm = s.distill(dctx, sessionID, "user: "+userText+"\n\nassistant: "+text, sensitiveRoute)
		dcancel()
		if tm != nil && (len(tm.FilesChanged) > 0 || len(tm.Failures) > 0 || len(tm.KeyFindings) > 0) {
			mctx, mcancel := context.WithTimeout(ctx, persistTimeout)
			if _, err := s.log.Append(mctx, sessionID, session.KindTurnMemory, session.TurnMemoryEvent{
				TurnSeq: turnSeq, TurnMemory: *tm,
			}); err != nil {
				s.logger.Warn("persist turn memory", "session_id", sessionID, "error", err)
			}
			mcancel()
		}
	}

	// Long-term memory extraction rides the same residue (D-007): the
	// user's words plus the distilled turn, never the raw trace. It
	// runs on every COMPLETED turn even when the assistant produced no
	// text (some providers end tool turns without a message): the
	// user's words alone can carry facts. The wrapper owns its timeout.
	if s.memory != nil && profile.Memory {
		mtext := "user: " + userText
		if tm != nil {
			if residue, err := json.Marshal(tm); err == nil {
				mtext += "\n\nturn residue: " + string(residue)
			}
		} else if text != "" {
			mtext += "\n\nassistant: " + text
		}
		s.memory(ctx, sessionID, turnSeq, mtext, sensitiveRoute, memoryContents)
	}

	if s.compactor != nil {
		cctx, cancel := context.WithTimeout(ctx, compactBudget)
		if err := s.postCompact(cctx, sessionID); err != nil {
			s.logger.Error("compaction", "session_id", sessionID, "error", err)
		}
		cancel()
	}
	if needsTitle {
		s.autoTitle(ctx, sessionID, userText, text, sensitiveRoute)
	}
}
