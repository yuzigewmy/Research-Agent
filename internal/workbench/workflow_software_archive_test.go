package workbench

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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

func TestSoftwareRegistrationPinsArchiveMembersAndRejectsUnsafePaths(t *testing.T) {
	const (
		zipSource = "package main\r\n\r\nfunc main() {\r\n\tprintln(\"原版观测记录\")  \r\n}"
		tarSource = "package river\n\nconst unit = \"cm\"\n"
		guide     = "从站点列表选择河岸测点后导入 CSV。"
		newSource = "package main\n// 新版观测记录\n"
	)
	type member struct{ path, text string }
	makeZIP := func(members ...member) []byte {
		t.Helper()
		var buf bytes.Buffer
		archive := zip.NewWriter(&buf)
		for _, entry := range members {
			w, err := archive.Create(entry.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(w, entry.text); err != nil {
				t.Fatal(err)
			}
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	makeTarGzip := func(entry member) []byte {
		t.Helper()
		var buf bytes.Buffer
		compressed := gzip.NewWriter(&buf)
		archive := tar.NewWriter(compressed)
		if err := archive.WriteHeader(&tar.Header{Name: entry.path, Mode: 0o644, Size: int64(len(entry.text))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(entry.text)); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []inference.Message }
		call := calls.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) == 0 || call != 1 {
			t.Errorf("unexpected software manual model call %d", call)
			http.Error(w, "unexpected input", http.StatusBadRequest)
			return
		}
		prompt := input.Messages[len(input.Messages)-1].Content
		if !strings.Contains(prompt, guide) || strings.Contains(prompt, "原版观测记录") || strings.Contains(prompt, "const unit") {
			t.Error("software manual should receive the archived guide excerpt and only metadata for source members")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# 操作手册\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	model, err := inference.New(inference.Config{BaseURL: upstream.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	handler := NewHandler(store, nil, HandlerConfig{Model: model})
	request := func(method, path, contentType string, body io.Reader, status int, out any) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+path, body)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		if out != nil {
			if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
				t.Fatalf("decode %s: %v", res.Body, err)
			}
		}
		return res.Body.String()
	}
	const create = `{"requestId":"software","title":"水位观测软件","templateId":"software_registration"}`
	var project Project
	request(http.MethodPost, projectsPath, "application/json", strings.NewReader(create), http.StatusCreated, &project)
	base := projectsPath + "/" + project.ProjectID
	workflow := base + "/workflow"
	upload := func(name string, data []byte) Material {
		t.Helper()
		var material Material
		request(http.MethodPost, base+"/materials?name="+url.QueryEscape(name), "application/octet-stream", bytes.NewReader(data), http.StatusCreated, &material)
		if material.MaterialID == "" || material.Name != name || material.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
			t.Fatalf("wrong upload receipt for %s: %+v", name, material)
		}
		return material
	}
	zipFile := upload("river.zip", makeZIP(member{"cmd/main.go", zipSource}, member{"docs/guide.md", guide}))
	tarFile := upload("helpers.tar.gz", makeTarGzip(member{"lib/unit.go", tarSource}))
	start := func(id string, materials ...string) {
		t.Helper()
		encoded, err := json.Marshal(materials)
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"requestId":%q,"checkpoints":true,"software":{"materialIds":%s}}`, id, encoded)
		request(http.MethodPost, workflow, "application/json", strings.NewReader(body), http.StatusCreated, nil)
	}
	waitFor := func(status string) WorkflowRun {
		t.Helper()
		for {
			var snapshot struct{ Run *WorkflowRun }
			request(http.MethodGet, workflow, "", nil, http.StatusOK, &snapshot)
			run := snapshot.Run
			if run != nil && run.Status == status {
				return *run
			}
			if run == nil || (run.Status != taskQueued && run.Status != taskRunning) || ctx.Err() != nil {
				t.Fatalf("want workflow status %s, got %+v", status, run)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	artifactPath := func(run WorkflowRun, step string) string {
		return workflow + "/" + run.RunID + "/steps/" + step + "/artifact"
	}

	start("archive-run", zipFile.MaterialID, tarFile.MaterialID)
	checkpoint := waitFor("WAITING_CHECKPOINT")
	wantMaterials := []TurnReference{
		{MaterialID: zipFile.MaterialID, Name: zipFile.Name, SHA256: zipFile.SHA256, Format: "archive"},
		{MaterialID: tarFile.MaterialID, Name: tarFile.Name, SHA256: tarFile.SHA256, Format: "archive"},
	}
	if checkpoint.Software == nil || !slices.Equal(checkpoint.Software.MaterialIDs, []string{zipFile.MaterialID, tarFile.MaterialID}) ||
		!slices.Equal(checkpoint.Software.Materials, wantMaterials) {
		t.Fatalf("workflow did not pin the uploaded archives: %+v", checkpoint.Software)
	}
	sources := request(http.MethodGet, artifactPath(checkpoint, "sources"), "", nil, http.StatusOK, nil)
	for _, path := range []string{"cmd/main.go", "docs/guide.md", "lib/unit.go"} {
		if !strings.Contains(sources, path) {
			t.Fatalf("sources did not enumerate %s: %s", path, sources)
		}
	}
	latest := upload(zipFile.Name, makeZIP(member{"cmd/main.go", newSource}))
	resume := fmt.Sprintf(`{"requestId":"resume","runId":%q,"expectedRevision":%d}`, checkpoint.RunID, checkpoint.Revision)
	request(http.MethodPost, workflow+"/continue", "application/json", strings.NewReader(resume), http.StatusAccepted, nil)
	finished := waitFor("COMPLETED")
	if finished.RunID != checkpoint.RunID || finished.Software == nil || !slices.Equal(finished.Software.Materials, wantMaterials) || calls.Load() != 1 {
		t.Fatalf("continuation changed the pinned run or dispatched extra model work: %+v, calls=%d", finished, calls.Load())
	}
	program := request(http.MethodGet, artifactPath(finished, "program"), "", nil, http.StatusOK, nil)
	sections, err := softwareProgramSections(program)
	if err != nil || len(sections) != 2 ||
		!slices.Contains(sections, softwarePDFSection{label: zipFile.Name + "/cmd/main.go", text: zipSource}) ||
		!slices.Contains(sections, softwarePDFSection{label: tarFile.Name + "/lib/unit.go", text: tarSource}) ||
		strings.Contains(program, latest.SHA256) {
		t.Fatalf("program materials did not preserve the two pinned members: %q (%v)", program, err)
	}

	unsafe := upload("unsafe.zip", makeZIP(member{"cmd/main.go", zipSource}, member{"src/../invalid.go", "package main\n"}))
	start("unsafe-run", unsafe.MaterialID)
	failed := waitFor("FAILED")
	if failed.RunID == finished.RunID || len(failed.Steps) != 4 || failed.Steps[0].Key != "sources" || failed.Steps[0].Status != taskFailed || calls.Load() != 1 {
		t.Fatalf("unsafe member did not fail during sources: %+v, calls=%d", failed, calls.Load())
	}
	for _, step := range failed.Steps[1:] {
		if step.Status != "PENDING" || step.ArtifactID != "" {
			t.Fatalf("unsafe archive allowed a downstream step to run: %+v", step)
		}
	}
}
