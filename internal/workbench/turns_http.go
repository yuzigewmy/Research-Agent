package workbench

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"research-agent/internal/inference"
)

//go:embed prompts/conversation.md
var conversationPrompt string

type conversation struct {
	store *Store
	model *inference.Client
}

func (c conversation) register(mux *http.ServeMux) {
	path := projectsPath + "/{id}/turns"
	mux.HandleFunc("GET "+path, c.list)
	mux.HandleFunc("POST "+path, c.send)
	mux.HandleFunc("GET "+path+"/{turnId}/stream", c.stream)
	mux.HandleFunc("POST "+path+"/{turnId}/stop", c.stop)
	mux.HandleFunc(path, methodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc(path+"/{turnId}/stream", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc(path+"/{turnId}/stop", methodNotAllowed("POST"))
	proposal := path + "/{turnId}/workflow-proposal"
	mux.HandleFunc("POST "+proposal+"/confirm", c.changeWorkflowProposal(true))
	mux.HandleFunc("POST "+proposal+"/cancel", c.changeWorkflowProposal(false))
}

func (c conversation) send(w http.ResponseWriter, r *http.Request) {
	var input sendTurnInput
	if !readJSON(w, r, &input, 128<<10) {
		return
	}
	if !validRequestID(input.RequestID) || strings.TrimSpace(input.Text) == "" || len(input.Text) > 16<<10 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请发送有效的请求编号和 16 KiB 以内的消息。")
		return
	}
	if !validTurnReferenceIDs(input.ReferenceIDs) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "每条消息可引用最多四份不同的资料。")
		return
	}
	if input.Mode != "" && !validTurnMode(input.Mode) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请选择仅问答或建议任务后确认。")
		return
	}
	turn, created, err := c.startReply(r.Context(), r.PathValue("id"), input)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
	case errors.Is(err, errRequestConflict):
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", "该请求编号已关联不同的消息、资料或协作方式。")
	case errors.Is(err, errInvalidTurnReference):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_REFERENCE", "请选择当前项目中支持正文预览的资料。")
	case errors.Is(err, errTurnBusy):
		writeError(w, http.StatusConflict, "TURN_RUNNING", "上一轮仍在回复。")
	case errors.Is(err, errModelUnavailable):
		writeError(w, http.StatusServiceUnavailable, "MODEL_UNAVAILABLE", "请先配置对话模型。")
	case err != nil:
		writeStoreError(w, "begin turn", err)
	case created:
		writeJSON(w, http.StatusAccepted, turn)
	default:
		writeJSON(w, http.StatusOK, turn)
	}
}

func (c conversation) reply(ctx context.Context, project Project, turn Turn) {
	proposal, generateErr := c.generate(ctx, project, turn)
	event := TurnEvent{Kind: "done", Status: "COMPLETED", proposal: proposal}
	if errors.Is(ctx.Err(), context.Canceled) {
		event.Status = "INTERRUPTED"
		event.Message = "服务关闭中断了此轮回复。可以重新发送。"
	} else if generateErr != nil {
		event.Status = "FAILED"
		switch {
		case errors.Is(generateErr, errMaterialNotText):
			event.Message = "无法读取引用资料的正文。请在资料预览中核对后重新发送。"
		case errors.Is(generateErr, errInvalidTaskProposal):
			event.Message = "任务建议缺少有效的说明。可重新发送消息。"
		default:
			event.Message = "回复未完成。请检查模型连接，或重新发送。"
		}
	}
	// A stopped turn already has a terminal event. The conditional write also
	// prevents late provider output from creating a proposal or syncing results.
	saveCtx, stop := context.WithTimeout(context.Background(), taskDBLimit)
	defer stop()
	if err := c.store.appendTurnEvent(saveCtx, turn.TurnID, event); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("finish conversation %s: %v", turn.TurnID, err)
		}
	} else if event.Status == "FAILED" {
		log.Printf("conversation %s: %v", turn.TurnID, generateErr)
	}
}

func (c conversation) generate(ctx context.Context, project Project, turn Turn) (turnProposal, error) {
	previous, err := c.store.listTurns(ctx, project.ProjectID, turn.ordinal, 6)
	if err != nil {
		return turnProposal{}, err
	}
	var workflow *workflowProposalRecord
	if turn.Mode == turnModeConfirm {
		workflow, err = c.store.workflowProposalOptions(ctx, project)
		if err != nil {
			return turnProposal{}, err
		}
	}
	projectData, err := json.Marshal(struct {
		Title        string                   `json:"title"`
		Template     string                   `json:"template"`
		Goal         string                   `json:"goal,omitempty"`
		Deliverables string                   `json:"deliverables,omitempty"`
		Workflow     []workflowStepDefinition `json:"runnableWorkflowSteps,omitempty"`
	}{project.Title, project.TemplateID, project.Goal, project.Deliverables, workflow.runnableSteps()})
	if err != nil {
		return turnProposal{}, err
	}
	messages := []inference.Message{{Role: "system", Content: strings.TrimSpace(conversationPrompt)},
		{Role: "user", Content: "以下 JSON 是当前项目资料。\n" + string(projectData)}}
	for i := len(previous) - 1; i >= 0; i-- {
		if previous[i].Status == "COMPLETED" {
			messages = append(messages, inference.Message{Role: "user", Content: previous[i].Text}, inference.Message{Role: "assistant", Content: previous[i].Answer})
		}
	}
	if len(turn.TaskResults) != 0 {
		results, err := c.store.turnTaskResultsContext(ctx, project.ProjectID, turn.TaskResults)
		if err != nil {
			return turnProposal{}, err
		}
		messages = append(messages, inference.Message{Role: "user", Content: results})
	}
	input, err := c.store.turnInput(ctx, turn)
	if err != nil {
		return turnProposal{}, err
	}
	messages = append(messages, inference.Message{Role: "user", Content: input})
	var bytesWritten int
	emit := func(delta string) error {
		bytesWritten += len(delta)
		if bytesWritten > 64<<10 {
			return errors.New("conversation output exceeds 64 KiB")
		}
		return c.store.appendTurnEvent(ctx, turn.TurnID, TurnEvent{Kind: "delta", Text: delta})
	}
	var arguments json.RawMessage
	if turn.Mode == turnModeConfirm {
		tool, err := proposalTool(workflow)
		if err != nil {
			return turnProposal{}, err
		}
		arguments, err = c.model.StreamWithTool(ctx, messages, tool, emit)
		if err != nil {
			return turnProposal{}, err
		}
	} else if err := c.model.Stream(ctx, messages, emit); err != nil {
		return turnProposal{}, err
	}
	if len(arguments) == 0 {
		if bytesWritten == 0 {
			return turnProposal{}, errors.New("model returned no text or task proposal")
		}
		return turnProposal{}, nil
	}
	proposal, err := parseTurnProposal(arguments, workflow)
	if err != nil {
		return turnProposal{}, err
	}
	if bytesWritten == 0 {
		message := "请核对助手建议的研究任务。"
		if proposal.workflow != nil {
			message = "请核对助手建议的工作流范围。"
		}
		if err := emit(message); err != nil {
			return turnProposal{}, err
		}
	}
	return proposal, nil
}

func (c conversation) list(w http.ResponseWriter, r *http.Request) {
	ctx, projectID := r.Context(), r.PathValue("id")
	before, err := eventCursor(r.URL.Query().Get("before"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "会话游标无效。")
		return
	}
	if _, err := c.store.getProject(ctx, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
		} else {
			writeStoreError(w, "lookup project for conversation", err)
		}
		return
	}
	const pageSize = 30
	items, err := c.store.listTurns(ctx, projectID, before, pageSize+1)
	if err != nil {
		writeStoreError(w, "list conversation", err)
		return
	}
	var next string
	if len(items) > pageSize {
		next = strconv.FormatInt(items[pageSize-1].ordinal, 10)
		items = items[:pageSize]
	}
	writeJSON(w, http.StatusOK, struct {
		Items           []Turn `json:"items"`
		NextCursor      string `json:"nextCursor,omitempty"`
		ModelConfigured bool   `json:"modelConfigured"`
	}{items, next, c.model != nil})
}

func eventCursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	seq, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seq < 0 {
		return 0, errors.New("invalid cursor")
	}
	return seq, nil
}

// Browser reconnects use Last-Event-ID, while a page reload uses its saved cursor.
// https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events#event_stream_format
func (c conversation) stream(w http.ResponseWriter, r *http.Request) {
	start, queryErr := eventCursor(r.URL.Query().Get("after"))
	last, headerErr := eventCursor(r.Header.Get("Last-Event-ID"))
	if queryErr != nil || headerErr != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "事件游标无效。")
		return
	}
	after := max(start, last)
	ctx, projectID, turnID := r.Context(), r.PathValue("id"), r.PathValue("turnId")
	turn, err := c.store.getTurn(ctx, projectID, turnID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "该项目下没有此轮对话。")
		} else {
			writeStoreError(w, "lookup conversation turn", err)
		}
		return
	}
	if turn.Status != "RUNNING" && after >= turn.AfterSeq {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		return
	}
	controller := http.NewResponseController(w)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil || controller.Flush() != nil {
		return
	}

	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	ping := time.NewTicker(12 * time.Second)
	defer ping.Stop()
	for {
		if turn.Status != "RUNNING" && after >= turn.AfterSeq {
			return
		}
		events, err := c.store.readTurnEvents(ctx, turnID, after)
		if err != nil {
			return
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.Seq, data); err != nil || controller.Flush() != nil {
				return
			}
			after = event.Seq
			if event.Kind == "done" {
				return
			}
		}
		if len(events) == streamBatch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		case <-poll.C:
		}
		turn, err = c.store.getTurn(ctx, projectID, turnID)
		if err != nil {
			return
		}
	}
}
