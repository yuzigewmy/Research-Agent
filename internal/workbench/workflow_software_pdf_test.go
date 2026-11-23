package workbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestSoftwareWorkflowPDFUsesRequestedArtifactAndPaginatesSource(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createProject := func(id, template string) Project {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: id, Title: "河流水位观测软件", TemplateID: template})
		if err != nil {
			t.Fatal(err)
		}
		return project
	}
	project := createProject("registration", "software_registration")
	other := createProject("other", "paper")

	programText := func(source string) string {
		t.Helper()
		file, _, err := store.saveMaterial(ctx, project.ProjectID, "main.go", strings.NewReader(source))
		if err != nil {
			t.Fatal(err)
		}
		inputs, err := store.readSoftwareInputs(ctx, []TurnReference{
			{MaterialID: file.MaterialID, Name: file.Name, SHA256: file.SHA256},
		})
		if err != nil {
			t.Fatal(err)
		}
		return softwareProgramMaterials(softwareProgramHeading, inputs.sources)
	}
	shortLines := programText("package main\n" + strings.Repeat("// 水位观测\n", 49) + "func main() {}")
	wrappedLines := programText("package main\n" + strings.Repeat("// 水位观测\n", 34) + "// " + strings.Repeat("W", 3000) + "\nfunc main() {}")
	const manual = "# 水位观测操作手册\n\n从站点列表选择测点，再导入观测数据。\n"

	// Save two program artifacts for the same workflow step using the rerun path.
	run, _, err := store.applyWorkflow(ctx, project.ProjectID, workflowStart, workflowRequest{
		RequestID: "start", ThroughStep: "program", Checkpoints: new(false),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	finish := func(projectID string, run WorkflowRun, key, body string) string {
		t.Helper()
		task, err := store.claimQueuedTask(ctx, projectID, true)
		if err != nil {
			t.Fatalf("claim %s: %v", key, err)
		}
		step := task.workflowStep
		if task.workflowRunID != run.RunID || step < 0 || step >= len(run.Steps) || run.Steps[step].Key != key {
			t.Fatalf("wrong task for %s: %+v", key, task)
		}
		if err := store.finishTask(ctx, projectID, task, taskSucceeded, "", taskResult{markdown: body}); err != nil {
			t.Fatalf("save %s artifact: %v", key, err)
		}
		return task.TaskID
	}
	sourcesID := finish(project.ProjectID, run, "sources", "# 已固定材料")
	manualID := finish(project.ProjectID, run, "manuscript", manual)
	historicalID := finish(project.ProjectID, run, "program", shortLines)
	current, err := store.latestWorkflow(ctx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.applyWorkflow(ctx, project.ProjectID, workflowRerun, workflowRequest{
		RequestID: "rerun-program", RunID: run.RunID, ExpectedRevision: current.Revision, StepKey: "program",
	}, true); err != nil {
		t.Fatal(err)
	}
	currentID := finish(project.ProjectID, run, "program", wrappedLines)

	paper, _, err := store.applyWorkflow(ctx, other.ProjectID, workflowStart, workflowRequest{
		RequestID: "start-paper", ThroughStep: "manuscript",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	finish(other.ProjectID, paper, "outline", "# 论文大纲")
	paperID := finish(other.ProjectID, paper, "manuscript", "# 论文稿件")

	artifactPath := func(projectID, runID, key, artifactID string) string {
		return projectsPath + "/" + projectID + "/workflow/" + runID + "/steps/" + key + "/artifacts/" + artifactID
	}
	get := func(t *testing.T, handler http.Handler, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+path, nil))
		if response.Code != want {
			t.Fatalf("GET %s: got HTTP %d, want %d: %.300s", path, response.Code, want, response.Body.Bytes())
		}
		return response
	}
	historicalPath := artifactPath(project.ProjectID, run.RunID, "program", historicalID)
	t.Run("missing font and invalid artifact scope", func(t *testing.T) {
		handler := NewHandler(store, nil, HandlerConfig{})
		for _, tc := range []struct {
			path string
			code string
			want int
		}{
			{historicalPath, "CAPABILITY_MISSING", http.StatusServiceUnavailable},
			{artifactPath(project.ProjectID, run.RunID, "program", "missing"), "NOT_FOUND", http.StatusNotFound},
			{artifactPath(project.ProjectID, run.RunID, "program", manualID), "NOT_FOUND", http.StatusNotFound},
			{artifactPath(other.ProjectID, run.RunID, "program", historicalID), "NOT_FOUND", http.StatusNotFound},
			{artifactPath(project.ProjectID, run.RunID, "sources", sourcesID), "NOT_FOUND", http.StatusNotFound},
			{artifactPath(other.ProjectID, paper.RunID, "manuscript", paperID), "NOT_FOUND", http.StatusNotFound},
		} {
			response := get(t, handler, tc.path+"/pdf", tc.want)
			var problem struct{ Error apiError }
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Error.Code != tc.code {
				t.Fatalf("GET %s/pdf: want %s, got %s (%v)", tc.path, tc.code, response.Body, err)
			}
		}
	})
	t.Run("artifact identity and page count", func(t *testing.T) {
		font := os.Getenv("SOFTWARE_PDF_TEST_FONT")
		if font == "" {
			t.Skip("set SOFTWARE_PDF_TEST_FONT to a local Chinese TrueType font")
		}
		handler := NewHandler(store, nil, HandlerConfig{SoftwarePDFFont: font})
		for _, tc := range []struct {
			key, artifactID, body string
			minPages, maxPages    int
		}{
			{"program", historicalID, shortLines, 2, 2},
			{"program", currentID, wrappedLines, 2, 0},
			{"manuscript", manualID, manual, 1, 1},
		} {
			path := artifactPath(project.ProjectID, run.RunID, tc.key, tc.artifactID)
			original := get(t, handler, path, http.StatusOK).Body.Bytes()
			if string(original) != tc.body {
				t.Fatalf("source for %s/%s did not match the saved artifact", tc.key, tc.artifactID)
			}
			response := get(t, handler, path+"/pdf", http.StatusOK)
			headers := response.Header()
			mediaType, _, typeErr := mime.ParseMediaType(headers.Get("Content-Type"))
			pages, countErr := strconv.Atoi(headers.Get("X-PDF-Page-Count"))
			if mediaType != "application/pdf" || typeErr != nil || !bytes.HasPrefix(response.Body.Bytes(), []byte("%PDF-")) ||
				headers.Get("X-Source-Artifact-ID") != tc.artifactID || headers.Get("X-Source-SHA256") != fmt.Sprintf("%x", sha256.Sum256(original)) {
				t.Fatalf("wrong PDF type or source binding for %s/%s: %v", tc.key, tc.artifactID, headers)
			}
			if countErr != nil || pages < tc.minPages || (tc.maxPages > 0 && pages > tc.maxPages) {
				t.Fatalf("wrong PDF page count for %s/%s: %q", tc.key, tc.artifactID, headers.Get("X-PDF-Page-Count"))
			}
		}
	})
}
