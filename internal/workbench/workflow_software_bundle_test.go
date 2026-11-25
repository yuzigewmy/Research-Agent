package workbench

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
)

func TestSoftwareWorkflowBundleBindsFilesToCompletedRevision(t *testing.T) {
	const title, version = "河流水位记录软件", "V2.1"
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project := func(requestID, template string) Project {
		t.Helper()
		p, _, err := store.createProject(ctx, createProjectInput{RequestID: requestID, Title: title, TemplateID: template})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	registration := project("registration", "software_registration")
	other := project("other", "software_registration")
	paper := project("paper", "paper")
	start := func(p Project, requestID string, options *softwareOptions) WorkflowRun {
		t.Helper()
		run, _, err := store.applyWorkflow(ctx, p.ProjectID, workflowStart, workflowRequest{
			RequestID: requestID, Checkpoints: new(false), Software: options,
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	finish := func(p Project, run WorkflowRun, key, body string) string {
		t.Helper()
		task, err := store.claimQueuedTask(ctx, p.ProjectID, true)
		if err != nil {
			t.Fatalf("claim %s: %v", key, err)
		}
		if task.workflowRunID != run.RunID || run.Steps[task.workflowStep].Key != key {
			t.Fatalf("wrong task claimed for %s: %+v", key, task)
		}
		if err := store.finishTask(ctx, p.ProjectID, task, taskSucceeded, "", taskResult{markdown: body}); err != nil {
			t.Fatalf("settle %s: %v", key, err)
		}
		return task.TaskID
	}
	digest := func(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }
	const source = "package main\r\n\r\nfunc main() {}\r\n"
	program := softwareProgramMaterials(softwareProgramHeading, []softwareSourceText{
		{name: "main.go", digest: digest([]byte(source)), body: source},
	})
	files := []struct{ key, name, body string }{
		{"sources", "software-registration-sources.md", "# 固定材料\n\n- main.go\n"},
		{"manuscript", "software-registration-manuscript.md", "# 操作手册\n\n导入水位观测记录。\n"},
		{"program", "software-registration-program.txt", program},
		{"application", "software-registration-application.txt", "软件全称 " + title + "\n版本号 " + version + "\n"},
	}
	run := start(registration, "complete", &softwareOptions{Version: version})
	artifactIDs := make(map[string]string, len(files))
	for _, file := range files {
		artifactIDs[file.name] = finish(registration, run, file.key, file.body)
	}
	completed, err := store.latestWorkflow(ctx, registration.ProjectID)
	if err != nil || completed.RunID != run.RunID || completed.Status != workflowDone {
		t.Fatalf("expected a completed registration run: %+v (%v)", completed, err)
	}
	updated, err := store.patchProject(ctx, registration.ProjectID, patchProjectInput{
		RequestID: "rename-software", ExpectedRevision: registration.Revision,
		Title: projectPatchField{sql.NullString{String: "流域观测记录软件", Valid: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := start(updated, "pending", nil)
	if pending.ProjectTitle != updated.Title {
		t.Fatalf("new run used a previous title: %+v", pending)
	}
	paperRun := start(paper, "paper", nil)
	for _, key := range []string{"outline", "manuscript", "structure"} {
		finish(paper, paperRun, key, "# Paper "+key+"\n")
	}
	paperRun, err = store.latestWorkflow(ctx, paper.ProjectID)
	if err != nil || paperRun.Status != workflowDone {
		t.Fatalf("expected a completed paper run: %+v (%v)", paperRun, err)
	}

	handler := NewHandler(store, nil, HandlerConfig{})
	bundlePath := func(p Project, run WorkflowRun, revision string) string {
		return projectsPath + "/" + p.ProjectID + "/workflow/runs/" + run.RunID + "/bundle?revision=" + revision
	}
	get := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+path, nil))
		if res.Code != status {
			t.Fatalf("GET %s: got HTTP %d, want %d: %.300s", path, res.Code, status, res.Body.Bytes())
		}
		return res
	}
	revision := strconv.Itoa(completed.Revision)
	response := get(bundlePath(registration, completed, revision), http.StatusOK)
	data := response.Body.Bytes()
	headers := response.Header()
	mediaType, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil || mediaType != "application/zip" || headers.Get("X-Workflow-Run-ID") != completed.RunID ||
		headers.Get("X-Workflow-Revision") != revision || headers.Get("X-Bundle-SHA256") != digest(data) {
		t.Fatalf("wrong bundle headers: %v", headers)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != len(files)+1 {
		t.Fatalf("expected four artifacts and a manifest in ZIP: %v", err)
	}
	manifestBody, err := fs.ReadFile(archive, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		RunID, WorkflowKey, Title, Version string
		Revision                           int
		Files                              []struct{ Name, ArtifactID, SourceSHA256, SHA256 string }
		Omitted                            []string
	}
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	slices.Sort(manifest.Omitted)
	if manifest.RunID != completed.RunID || manifest.Revision != completed.Revision || manifest.WorkflowKey != completed.WorkflowKey ||
		manifest.Title != title || manifest.Version != version || len(manifest.Files) != len(files) || !slices.Equal(manifest.Omitted, []string{"docx", "pdf"}) {
		t.Fatalf("wrong manifest: %s", manifestBody)
	}
	for _, file := range files {
		body, err := fs.ReadFile(archive, file.name)
		if err != nil || string(body) != file.body {
			t.Fatalf("wrong bundled %s: %q (%v)", file.name, body, err)
		}
		index := slices.IndexFunc(manifest.Files, func(item struct{ Name, ArtifactID, SourceSHA256, SHA256 string }) bool {
			return item.Name == file.name
		})
		if index < 0 {
			t.Fatalf("manifest omitted %s", file.name)
		}
		entry := manifest.Files[index]
		if entry.ArtifactID != artifactIDs[file.name] || entry.SourceSHA256 != digest([]byte(file.body)) || entry.SHA256 != digest(body) {
			t.Fatalf("wrong manifest record for %s: %+v", file.name, entry)
		}
	}

	t.Run("reuses rendered PDF across downloads and bundles", func(t *testing.T) {
		font := os.Getenv("SOFTWARE_PDF_TEST_FONT")
		if font == "" {
			t.Skip("set SOFTWARE_PDF_TEST_FONT to a local Chinese TrueType font")
		}
		handler = NewHandler(store, nil, HandlerConfig{SoftwarePDFFont: font})
		artifact := projectsPath + "/" + registration.ProjectID + "/workflow/" + completed.RunID +
			"/steps/program/artifacts/" + artifactIDs["software-registration-program.txt"]
		first := get(artifact+"/pdf", http.StatusOK)
		if first.Header().Get("X-PDF-Cache") != "miss" {
			t.Fatalf("first render had unexpected cache status: %v", first.Header())
		}
		bundle := get(bundlePath(registration, completed, revision), http.StatusOK)
		archive, err := zip.NewReader(bytes.NewReader(bundle.Body.Bytes()), int64(bundle.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		pdf, err := fs.ReadFile(archive, "software-registration-program.pdf")
		if err != nil || !bytes.Equal(pdf, first.Body.Bytes()) {
			t.Fatalf("bundle did not reuse the single-file PDF: %v", err)
		}
		if reused := get(artifact+"/pdf", http.StatusOK); reused.Header().Get("X-PDF-Cache") != "hit" || !bytes.Equal(reused.Body.Bytes(), pdf) {
			t.Fatalf("repeat download did not reuse the cached PDF: %v", reused.Header())
		}

		usage, err := store.workspaceStorage(ctx)
		if err != nil || usage.CacheCount != 2 || usage.CacheBytes <= int64(len(pdf)) || usage.CacheUnavailable {
			t.Fatalf("cache usage did not include both rendered PDFs: %+v (%v)", usage, err)
		}
		var backup bytes.Buffer
		if err := store.writeWorkspaceBackup(ctx, t.TempDir(), &backup); err != nil {
			t.Fatal(err)
		}
		backupZIP, err := zip.NewReader(bytes.NewReader(backup.Bytes()), int64(backup.Len()))
		if err != nil || len(backupZIP.File) != 2 {
			t.Fatalf("backup included rebuildable cache files: %v", err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1/api/v1/workspace-cache/clear", nil))
		var receipt struct {
			RemovedBytes int64
			RemovedCount int
		}
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); response.Code != http.StatusOK || err != nil ||
			receipt.RemovedCount != usage.CacheCount || receipt.RemovedBytes != usage.CacheBytes {
			t.Fatalf("clear receipt did not match cache usage: %d %s (%v)", response.Code, response.Body, err)
		}
		if used, err := store.workspaceStorage(ctx); err != nil || used.CacheCount != 0 || used.CacheBytes != 0 {
			t.Fatalf("cache stayed after clear: %+v (%v)", used, err)
		}
		if got := get(artifact, http.StatusOK).Body.String(); got != program {
			t.Fatal("clearing cache changed the source artifact")
		}
		if rebuilt := get(artifact+"/pdf", http.StatusOK); rebuilt.Header().Get("X-PDF-Cache") != "miss" || !bytes.HasPrefix(rebuilt.Body.Bytes(), []byte("%PDF-")) {
			t.Fatalf("PDF could not be rebuilt after clear: %v", rebuilt.Header())
		}
	})

	for _, tc := range []struct {
		path, code string
		status     int
	}{
		{bundlePath(registration, completed, "not-a-revision"), "", http.StatusBadRequest},
		{bundlePath(registration, completed, strconv.Itoa(completed.Revision-1)), "REVISION_CONFLICT", http.StatusConflict},
		{bundlePath(registration, pending, strconv.Itoa(pending.Revision)), "WORKFLOW_STATE_CONFLICT", http.StatusConflict},
		{bundlePath(other, completed, revision), "", http.StatusNotFound},
		{bundlePath(paper, paperRun, strconv.Itoa(paperRun.Revision)), "", http.StatusNotFound},
	} {
		res := get(tc.path, tc.status)
		if tc.code != "" {
			var problem struct{ Error apiError }
			if err := json.Unmarshal(res.Body.Bytes(), &problem); err != nil || problem.Error.Code != tc.code {
				t.Fatalf("GET %s: want %s, got %s (%v)", tc.path, tc.code, res.Body, err)
			}
		}
	}
}
