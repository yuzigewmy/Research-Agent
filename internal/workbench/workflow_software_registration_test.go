package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"research-agent/internal/inference"
)

func TestSoftwareRegistrationKeepsUploadedSourceThroughCheckpoint(t *testing.T) {
	const (
		title       = "河流水位观测记录管理软件"
		description = "按站点汇总每天的水位读数。"
		document    = "操作说明：从站点列表选择测点后导入 CSV。"
		source      = "package main\r\n\r\nfunc main() {\r\n\tprintln(\"固定源码：河岸水位\")  \r\n}"
		newMarker   = "替换后的源码"
		manual      = "# 操作手册\n\n从站点列表选择测点，再导入观测数据。"
	)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	handler := NewHandler(store, nil, HandlerConfig{})
	serve := func(req *http.Request, status int, out any) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", req.Method, req.URL.RequestURI(), res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res
	}
	request := func(method, path, body string, status int, out any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return serve(req, status, out)
	}
	var project Project
	request(http.MethodPost, projectsPath, fmt.Sprintf(`{"requestId":"registration","templateId":"software_registration","title":%q}`, title), http.StatusCreated, &project)
	projectPath := projectsPath + "/" + project.ProjectID
	upload := func(name, body string) Material {
		t.Helper()
		path := projectPath + "/materials?name=" + url.QueryEscape(name)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/octet-stream")
		var material Material
		serve(req, http.StatusCreated, &material)
		return material
	}
	guide := upload("guide.md", document)
	program := upload("main.go", source)

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var input struct{ Messages []inference.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 || call != 1 {
			t.Errorf("unexpected manual generation call %d", call)
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		for _, want := range []string{title, "V1.0", description, document, guide.Name, guide.MaterialID, guide.SHA256, program.Name, program.MaterialID, program.SHA256} {
			if !strings.Contains(prompt, want) {
				t.Errorf("manual prompt is missing %q", want)
			}
		}
		for _, message := range input.Messages {
			if strings.Contains(message.Content, "固定源码：河岸水位") {
				t.Error("manual prompt included program source text")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", manual)
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(store, nil, HandlerConfig{Model: model})
	workflow := projectPath + "/workflow"
	type registrationRun struct {
		WorkflowRun
		Software *struct {
			Version, Description string
			MaterialIDs          []string
			Materials            []TurnReference
		} `json:"software"`
	}
	waitFor := func(status string) *registrationRun {
		t.Helper()
		for {
			var response struct{ Run *registrationRun }
			request(http.MethodGet, workflow, "", http.StatusOK, &response)
			if run := response.Run; run != nil && run.Status == status {
				return run
			}
			if run := response.Run; run == nil || (run.Status != taskQueued && run.Status != taskRunning) || ctx.Err() != nil {
				t.Fatalf("want workflow status %s, got %+v", status, run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	start := fmt.Sprintf(`{"requestId":"start","software":{"version":" ","description":%q,"materialIds":[%q,%q]}}`, description, guide.MaterialID, program.MaterialID)
	var started registrationRun
	request(http.MethodPost, workflow, start, http.StatusCreated, &started)
	checkpoint := waitFor("WAITING_CHECKPOINT")
	wantIDs := []string{guide.MaterialID, program.MaterialID}
	wantMaterials := []TurnReference{
		{MaterialID: guide.MaterialID, Name: guide.Name, SHA256: guide.SHA256, Format: guide.Format},
		{MaterialID: program.MaterialID, Name: program.Name, SHA256: program.SHA256, Format: program.Format},
	}
	settings := checkpoint.Software
	if started.RunID == "" || checkpoint.RunID != started.RunID || !checkpoint.Checkpoints || settings == nil || settings.Version != "V1.0" ||
		settings.Description != description || !slices.Equal(settings.MaterialIDs, wantIDs) || !slices.Equal(settings.Materials, wantMaterials) {
		t.Fatalf("wrong registration receipt: run=%+v, software=%+v", checkpoint, settings)
	}
	keys := []string{"sources", "manuscript", "program", "application"}
	if len(checkpoint.Steps) != len(keys) || calls.Load() != 1 {
		t.Fatalf("wrong checkpoint steps: %+v, model calls=%d", checkpoint.Steps, calls.Load())
	}
	for i, key := range keys {
		status := "PENDING"
		if i < 2 {
			status = taskSucceeded
		}
		if step := checkpoint.Steps[i]; step.Key != key || step.Status != status {
			t.Fatalf("wrong checkpoint state for %s: %+v", key, step)
		}
	}
	latest := upload(program.Name, "package main\n\nfunc main() { println(\""+newMarker+"\") }\n")
	var replay registrationRun
	request(http.MethodPost, workflow, start, http.StatusOK, &replay)
	if replay.RunID != checkpoint.RunID || replay.Revision != checkpoint.Revision || replay.Status != checkpoint.Status || calls.Load() != 1 {
		t.Fatalf("start replay changed the checkpoint: %+v, model calls=%d", replay, calls.Load())
	}
	resume := fmt.Sprintf(`{"requestId":"continue","runId":%q,"expectedRevision":%d}`, checkpoint.RunID, checkpoint.Revision)
	request(http.MethodPost, workflow+"/continue", resume, http.StatusAccepted, nil)
	finished := waitFor("COMPLETED")
	if finished.RunID != checkpoint.RunID || calls.Load() != 1 {
		t.Fatalf("continuation did not finish the saved run: %+v, model calls=%d", finished, calls.Load())
	}

	runPath := workflow + "/" + finished.RunID
	downloadText := func(step, filename string) string {
		t.Helper()
		res := request(http.MethodGet, runPath+"/steps/"+step+"/artifact", "", http.StatusOK, nil)
		mediaType, _, typeErr := mime.ParseMediaType(res.Header().Get("Content-Type"))
		disposition, params, nameErr := mime.ParseMediaType(res.Header().Get("Content-Disposition"))
		if typeErr != nil || nameErr != nil || mediaType != "text/plain" || disposition != "attachment" || params["filename"] != filename {
			t.Fatalf("invalid %s download headers: %v", step, res.Header())
		}
		return res.Body.String()
	}
	programText := downloadText("program", "software-registration-program.txt")
	sourceSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	if !strings.Contains(programText, source) || !strings.Contains(programText, sourceSHA) ||
		strings.Contains(programText, newMarker) || strings.Contains(programText, latest.SHA256) {
		t.Fatalf("program materials did not preserve the pinned source bytes and digest: %q", programText)
	}
	application := downloadText("application", "software-registration-application.txt")
	if !strings.Contains(application, title) || !strings.Contains(application, "V1.0") || !strings.Contains(application, "待核实") {
		t.Fatalf("wrong registration checklist: %s", application)
	}
	var file WorkspaceFile
	request(http.MethodPost, runPath+"/manuscript", "", http.StatusOK, &file)
	var saved WorkspaceDocument
	request(http.MethodGet, projectPath+"/files/versions/"+file.VersionID, "", http.StatusOK, &saved)
	if saved.VersionID != file.VersionID || saved.SHA256 != file.SHA256 || saved.Content != manual {
		t.Fatalf("workspace manuscript does not match the manual: %+v", saved)
	}
}
