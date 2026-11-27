package workbench

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
)

const maxComputeScatterBytes = 4 << 20

const (
	computeReportName  = "comparison.json"
	computeScatterName = "scatter.svg"
)

type computeArtifact struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type computeArtifactManifest struct {
	JobID       string            `json:"jobId"`
	InputSHA256 string            `json:"inputSha256"`
	Items       []computeArtifact `json:"items"`
}

func (worker *ComputeWorker) listArtifacts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("jobId")
	if !validComputeJobID(id) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "作业编号无效。")
		return
	}
	manifest := computeArtifactManifest{JobID: id}
	report, scatter := computeArtifact{Name: computeReportName}, computeArtifact{Name: computeScatterName}
	err := worker.db.QueryRowContext(r.Context(), `SELECT input_sha256, result_sha256, length(CAST(result_json AS BLOB)),
		scatter_sha256, length(CAST(scatter_svg AS BLOB)) FROM jobs WHERE job_id = ? AND status = 'SUCCEEDED'`, id).
		Scan(&manifest.InputSHA256, &report.SHA256, &report.Size, &scatter.SHA256, &scatter.Size)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "RESULT_NOT_FOUND", "此作业没有可取回的成果。")
		return
	}
	if err != nil {
		writeStoreError(w, "list compute artifacts", err)
		return
	}
	manifest.Items = []computeArtifact{report}
	if scatter.Size != 0 {
		manifest.Items = append(manifest.Items, scatter)
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (worker *ComputeWorker) readScatter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("jobId")
	if !validComputeJobID(id) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "作业编号无效。")
		return
	}
	var scatter, digest string
	err := worker.db.QueryRowContext(r.Context(), `SELECT scatter_svg, scatter_sha256 FROM jobs
		WHERE job_id = ? AND status = 'SUCCEEDED' AND scatter_sha256 <> ''`, id).Scan(&scatter, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "RESULT_NOT_FOUND", "此作业没有可取回的散点图。")
		return
	}
	if err != nil {
		writeStoreError(w, "read compute scatter", err)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="scatter.svg"`)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	w.Header().Set("X-Result-SHA256", digest)
	_, _ = io.WriteString(w, scatter)
}
