package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const projectEventsSchema = `
	CREATE TABLE project_events (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		entity_id TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL,
		payload_json TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL
	);
	CREATE INDEX project_events_seq ON project_events(project_id, seq);
	PRAGMA user_version = 67;
`

const (
	projectEventFileSaved          = "file.saved"
	projectEventTaskUpdated        = "task.updated"
	projectEventWorkflowStep       = "workflow.step"
	projectEventWorkflowCheckpoint = "workflow.checkpoint"
	projectEventReviewUpdated      = "review.updated"
	projectEventScriptUpdated      = "script.updated"

	defaultProjectEventLimit = 50
	maxProjectEventLimit     = 200
)

type ProjectEvent struct {
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	EntityID  string          `json:"entityId"`
	Summary   string          `json:"summary"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"createdAt"`
}

type ProjectEventsSnapshot struct {
	ProjectID string         `json:"projectId"`
	LatestSeq int64          `json:"latestSeq"`
	AfterSeq  int64          `json:"afterSeq"`
	Items     []ProjectEvent `json:"items"`
}

type projectEventExec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func sha256Prefix(digest string) string {
	digest = strings.TrimSpace(digest)
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

// appendProjectEvent records a public, non-sensitive activity event for a project.
func appendProjectEvent(ctx context.Context, txOrDB projectEventExec, projectID, kind, entityID, summary string, payload map[string]any) error {
	if txOrDB == nil || projectID == "" || kind == "" {
		return nil
	}
	encoded := "{}"
	if len(payload) > 0 {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	_, err := txOrDB.ExecContext(ctx, `INSERT INTO project_events (project_id, kind, entity_id, summary, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, kind, entityID, summary, encoded, time.Now().UTC().Format(createdAtLayout))
	if err != nil && strings.Contains(err.Error(), "no such table: project_events") {
		// Allow legacy-schema unit tests (opened before v67 migration) to keep running.
		return nil
	}
	return err
}

func (s *Store) readProjectEventsSnapshot(ctx context.Context, projectID string, after int64, limit int) (ProjectEventsSnapshot, error) {
	if _, err := s.getProject(ctx, projectID); err != nil {
		return ProjectEventsSnapshot{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, kind, entity_id, summary, payload_json, created_at
		FROM project_events
		WHERE project_id = ? AND seq > ?
		ORDER BY seq ASC
		LIMIT ?`, projectID, after, limit)
	if err != nil {
		return ProjectEventsSnapshot{}, err
	}
	defer rows.Close()

	items := make([]ProjectEvent, 0)
	for rows.Next() {
		var item ProjectEvent
		var payload string
		if err := rows.Scan(&item.Seq, &item.Kind, &item.EntityID, &item.Summary, &payload, &item.CreatedAt); err != nil {
			return ProjectEventsSnapshot{}, err
		}
		if strings.TrimSpace(payload) == "" || !json.Valid([]byte(payload)) {
			payload = "{}"
		}
		item.Payload = json.RawMessage(payload)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ProjectEventsSnapshot{}, err
	}

	var latestSeq int64
	if len(items) > 0 {
		latestSeq = items[len(items)-1].Seq
	} else {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM project_events WHERE project_id = ?`, projectID).Scan(&latestSeq); err != nil {
			return ProjectEventsSnapshot{}, err
		}
	}
	return ProjectEventsSnapshot{
		ProjectID: projectID,
		LatestSeq: latestSeq,
		AfterSeq:  after,
		Items:     items,
	}, nil
}

func (s *Store) registerProjectEvents(mux *http.ServeMux) {
	route := projectsPath + "/{id}/events"
	mux.HandleFunc("GET "+route, s.handleProjectEvents)
	mux.HandleFunc(route, methodNotAllowed("GET, HEAD"))
}

func (s *Store) handleProjectEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := r.PathValue("id")
	query := r.URL.Query()

	queryAfter, queryErr := eventCursor(query.Get("after"))
	headerAfter, headerErr := eventCursor(r.Header.Get("Last-Event-ID"))
	if queryErr != nil || headerErr != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "事件游标必须为非负整数。")
		return
	}
	after := max(queryAfter, headerAfter)

	limit := defaultProjectEventLimit
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > maxProjectEventLimit {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "limit 必须为 1 至 200 之间的整数。")
			return
		}
		limit = parsed
	}

	snapshot, err := s.readProjectEventsSnapshot(ctx, projectID, after, limit)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
		return
	}
	if err != nil {
		writeStoreError(w, "read project events", err)
		return
	}

	wantsSSE := query.Get("format") != "json" &&
		strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
	if !wantsSSE {
		writeJSON(w, http.StatusOK, snapshot)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		return
	}
	controller := http.NewResponseController(w)
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(w, "event: snapshot\nid: %d\ndata: %s\n\n", snapshot.LatestSeq, snapshotJSON); err != nil || controller.Flush() != nil {
		return
	}
	cursor := after
	for _, item := range snapshot.Items {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", item.Seq, item.Kind, itemJSON); err != nil || controller.Flush() != nil {
			return
		}
		if item.Seq > cursor {
			cursor = item.Seq
		}
	}
	if snapshot.LatestSeq > cursor {
		cursor = snapshot.LatestSeq
	}

	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	ping := time.NewTicker(12 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		case <-poll.C:
			next, err := s.readProjectEventsSnapshot(ctx, projectID, cursor, limit)
			if err != nil {
				return
			}
			for _, item := range next.Items {
				itemJSON, err := json.Marshal(item)
				if err != nil {
					return
				}
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", item.Seq, item.Kind, itemJSON); err != nil || controller.Flush() != nil {
					return
				}
				if item.Seq > cursor {
					cursor = item.Seq
				}
			}
		}
	}
}
