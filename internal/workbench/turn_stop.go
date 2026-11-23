package workbench

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"
)

const turnStopped = "STOPPED"

const stoppedReplyMessage = "已停止回复。已生成的内容已保留。"

type turnWorkers struct {
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	closing bool
	wg      sync.WaitGroup
}

func (c conversation) startReply(ctx context.Context, projectID string, input sendTurnInput) (Turn, bool, error) {
	workers := &c.store.turns
	workers.mu.Lock()
	defer workers.mu.Unlock()
	if workers.closing {
		return Turn{}, false, context.Canceled
	}
	// Stop and start share this lock so a saved turn always has a cancel function.
	turn, project, created, err := c.store.beginTurn(ctx, projectID, input, c.model != nil)
	if err != nil || !created {
		return turn, false, err
	}
	replyCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	workers.active[turn.TurnID] = cancel
	workers.wg.Add(1)
	go func() {
		defer workers.wg.Done()
		defer cancel()
		c.reply(replyCtx, project, turn)
		workers.mu.Lock()
		delete(workers.active, turn.TurnID)
		workers.mu.Unlock()
	}()
	return turn, true, nil
}

func (workers *turnWorkers) close() {
	workers.mu.Lock()
	workers.closing = true
	for _, cancel := range workers.active {
		cancel()
	}
	workers.mu.Unlock()
	workers.wg.Wait()
}

func (c conversation) stop(w http.ResponseWriter, r *http.Request) {
	turn, err := c.store.stopTurn(r.Context(), r.PathValue("id"), r.PathValue("turnId"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目下没有此轮对话。")
	case err != nil:
		writeStoreError(w, "stop conversation", err)
	default:
		writeJSON(w, http.StatusOK, turn)
	}
}

func (s *Store) stopTurn(ctx context.Context, projectID, turnID string) (Turn, error) {
	workers := &s.turns
	workers.mu.Lock()
	defer workers.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Turn{}, err
	}
	defer tx.Rollback()
	turn, err := readTurn(ctx, tx, projectID, turnID)
	if err != nil || turn.Status != "RUNNING" {
		return turn, err
	}
	event := TurnEvent{Kind: "done", Status: turnStopped, Message: stoppedReplyMessage}
	seq, err := recordTurnEvent(ctx, tx, turnID, &event)
	if err != nil {
		return Turn{}, err
	}
	if err := tx.Commit(); err != nil {
		return Turn{}, err
	}
	if cancel := workers.active[turnID]; cancel != nil {
		cancel()
	}
	turn.Status, turn.Message, turn.AfterSeq = event.Status, event.Message, seq
	return turn, nil
}
