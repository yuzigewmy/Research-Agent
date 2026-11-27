package workbench

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const remoteScatterSchema = `
	ALTER TABLE remote_comparisons ADD COLUMN scatter_size INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE remote_comparisons ADD COLUMN scatter_sha256 TEXT NOT NULL DEFAULT '';
	ALTER TABLE remote_comparisons ADD COLUMN scatter_svg TEXT NOT NULL DEFAULT '';
	PRAGMA user_version = 63;
`

type remoteScatter struct {
	Size      int    `json:"size"`
	SHA256    string `json:"sha256"`
	Retrieved bool   `json:"retrieved"`
}

func (node *ComputeNode) scatterMetadata(ctx context.Context, job computeJob) (remoteScatter, error) {
	body, _, err := node.jobRequest(ctx, http.MethodGet, job.JobID, "artifacts", "", "")
	if err != nil {
		return remoteScatter{}, err
	}
	var manifest computeArtifactManifest
	if json.Unmarshal(body, &manifest) != nil || manifest.JobID != job.JobID || manifest.InputSHA256 != job.InputSHA256 ||
		len(manifest.Items) < 1 || len(manifest.Items) > 2 {
		return remoteScatter{}, errRemoteResponse
	}
	// The report is always first. Older jobs may list only the report.
	report := manifest.Items[0]
	if report.Name != computeReportName || report.SHA256 != job.ResultSHA256 || report.Size <= 0 || report.Size > 8<<10 {
		return remoteScatter{}, errRemoteResponse
	}
	if len(manifest.Items) == 1 {
		return remoteScatter{}, nil
	}
	scatter := manifest.Items[1]
	if scatter.Name != computeScatterName || scatter.Size <= 0 || scatter.Size > maxComputeScatterBytes || !validObjectDigest(scatter.SHA256) {
		return remoteScatter{}, errRemoteResponse
	}
	return remoteScatter{Size: scatter.Size, SHA256: scatter.SHA256}, nil
}

func (s *Store) updateRemoteScatter(w http.ResponseWriter, r *http.Request, node *ComputeNode, job remoteComparison, report experimentComparisonReport, action string) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	projectID, taskID := r.PathValue("id"), r.PathValue("taskId")
	if action == "artifacts" {
		meta, err := node.scatterMetadata(ctx, job.computeJob)
		if err == errRemoteMissing && job.scatter.Size == 0 {
			writeRemoteComparison(w, node, job)
			return
		}
		if err != nil || (job.scatter.Size != 0 && meta != (remoteScatter{Size: job.scatter.Size, SHA256: job.scatter.SHA256})) {
			writeError(w, http.StatusBadGateway, "COMPUTE_ARTIFACTS_UNVERIFIED", "暂时无法核对远程成果清单，请重试。")
			return
		}
		if meta.Size != 0 {
			if _, err := s.db.ExecContext(ctx, `UPDATE remote_comparisons SET scatter_size = ?, scatter_sha256 = ?
				WHERE project_id = ? AND task_id = ? AND scatter_sha256 = ''`, meta.Size, meta.SHA256, projectID, taskID); err != nil {
				writeStoreError(w, "save scatter metadata", err)
				return
			}
		}
	} else {
		body, headers, err := node.jobRequest(ctx, http.MethodGet, job.JobID, "artifacts/scatter.svg", "", "")
		if err != nil || len(body) != job.scatter.Size || headers.Get("X-Result-SHA256") != job.scatter.SHA256 || computeDigest(body) != job.scatter.SHA256 {
			writeError(w, http.StatusBadGateway, "COMPUTE_SCATTER_UNVERIFIED", "暂时无法取回或校验散点图，请重试。")
			return
		}
		source, err := s.readExperimentSource(ctx, projectID, report.Input)
		if err != nil {
			writeExperimentEvidenceError(w, errExperimentEvidence)
			return
		}
		report.Input.VersionID = ""
		expected, err := renderExperimentPlot(ctx, report, source)
		if err != nil || string(body) != expected {
			writeError(w, http.StatusBadGateway, "COMPUTE_SCATTER_UNVERIFIED", "远程散点图与固定输入的复算结果不一致。")
			return
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE remote_comparisons SET scatter_svg = ? WHERE project_id = ? AND task_id = ? AND scatter_sha256 = ?`,
			string(body), projectID, taskID, job.scatter.SHA256); err != nil {
			writeStoreError(w, "save remote scatter", err)
			return
		}
	}
	job, err := readRemoteComparison(ctx, s.db, projectID, taskID)
	if err != nil {
		writeStoreError(w, "read remote scatter receipt", err)
		return
	}
	writeRemoteComparison(w, node, job)
}

func (s *Store) serveRemoteScatter(w http.ResponseWriter, r *http.Request, job remoteComparison, saveMaterial bool) {
	var plot string
	if err := s.db.QueryRowContext(r.Context(), `SELECT scatter_svg FROM remote_comparisons WHERE project_id = ? AND task_id = ?`,
		r.PathValue("id"), r.PathValue("taskId")).Scan(&plot); err != nil {
		writeStoreError(w, "read remote scatter", err)
		return
	}
	if len(plot) != job.scatter.Size || computeDigest([]byte(plot)) != job.scatter.SHA256 {
		writeError(w, http.StatusNotFound, "RESULT_NOT_FOUND", "没有通过校验的远程散点图。")
		return
	}
	if saveMaterial {
		name := "远程散点图-" + strings.ToLower(r.PathValue("taskId")) + ".svg"
		material, _, err := s.saveMaterial(r.Context(), r.PathValue("id"), name, strings.NewReader(plot))
		if err != nil {
			writeMaterialError(w, "save remote scatter material", err)
			return
		}
		writeJSON(w, http.StatusOK, material)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="scatter.svg"`)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Result-SHA256", job.scatter.SHA256)
	_, _ = io.WriteString(w, plot)
}
