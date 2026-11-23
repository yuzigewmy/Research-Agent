package workbench

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type softwarePDFSection struct {
	label string
	text  string
}

var errSoftwareProgramFormat = errors.New("invalid program material record")

// The byte count separates each source from the framing of the saved TXT.
func softwareProgramSections(program string) ([]softwarePDFSection, error) {
	body, ok := strings.CutPrefix(program, softwareProgramHeading)
	if !ok {
		return nil, errSoftwareProgramFormat
	}
	if body == softwareProgramMissing {
		return []softwarePDFSection{{text: body}}, nil
	}
	var sections []softwarePDFSection
	for body != "" {
		header, rest, found := strings.Cut(body, "\n\n")
		fields := strings.Split(header, "\n")
		if !found || len(fields) != 3 {
			return nil, errSoftwareProgramFormat
		}
		name, nameOK := strings.CutPrefix(fields[0], "文件 ")
		digest, digestOK := strings.CutPrefix(fields[1], "SHA-256 ")
		size, sizeOK := strings.CutPrefix(fields[2], "字节数 ")
		count, err := strconv.Atoi(size)
		if !nameOK || !digestOK || !sizeOK || err != nil || count < 0 || count > len(rest) {
			return nil, errSoftwareProgramFormat
		}
		text := rest[:count]
		body, found = strings.CutPrefix(rest[count:], "\n\n")
		if !found || fmt.Sprintf("%x", sha256.Sum256([]byte(text))) != digest {
			return nil, errSoftwareProgramFormat
		}
		sections = append(sections, softwarePDFSection{label: name, text: text})
	}
	if len(sections) == 0 {
		return nil, errSoftwareProgramFormat
	}
	return sections, nil
}

func (api workflows) softwareIdentificationPDF(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, runID := r.Context(), r.PathValue("id"), r.PathValue("runId")
	key, artifactID := r.PathValue("key"), r.PathValue("artifactId")
	kind := "文档鉴别材料"
	if key == "program" {
		kind = "程序鉴别材料"
	} else if key != "manuscript" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "此步骤没有鉴别材料 PDF。")
		return
	}
	var title, options string
	if err := api.store.db.QueryRowContext(ctx, `SELECT p.title, w.options_json FROM workflow_runs w
		JOIN projects p ON p.project_id = w.project_id WHERE w.project_id = ? AND w.run_id = ? AND w.workflow_key = ?`,
		projectID, runID, softwareRegistrationWorkflow).Scan(&title, &options); err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	step, _, err := workflowArtifactStep(ctx, api.store.db, projectID, runID, key)
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	artifact, err := lookupWorkflowArtifact(ctx, api.store.db, projectID, runID, step, artifactID)
	if err != nil {
		writeWorkflowArtifactError(w, err)
		return
	}
	if api.softwarePDFFont == "" {
		writeError(w, http.StatusServiceUnavailable, "CAPABILITY_MISSING", "请设置 RESEARCH_PDF_FONT，指定可嵌入的 TrueType 字体。")
		return
	}
	var settings softwareSettings
	if err := json.Unmarshal([]byte(options), &settings); err != nil {
		writeStoreError(w, "read software PDF settings", err)
		return
	}
	sections := []softwarePDFSection{{text: artifact.markdown}}
	if key == "program" {
		sections, err = softwareProgramSections(artifact.markdown)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "INVALID_ARTIFACT", "无法核对程序材料中的源码及指纹。")
			return
		}
	}
	body, pages, err := renderSoftwarePDF(ctx, api.softwarePDFFont, kind, title, settings.Version, sections)
	switch {
	case errors.Is(err, errSoftwarePDFFont):
		writeError(w, http.StatusServiceUnavailable, "PDF_FONT_UNAVAILABLE", "无法加载配置的 TrueType 字体。请检查字体文件和嵌入权限。")
		return
	case errors.Is(err, errSoftwarePDFGlyph):
		writeError(w, http.StatusUnprocessableEntity, "PDF_GLYPH_MISSING", "当前字体缺少材料所用的字符。请换用包含这些字符的可嵌入字体。")
		return
	case err != nil:
		writeStoreError(w, "render software identification PDF", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="software-registration-`+key+`.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Source-Artifact-ID", artifact.ArtifactID)
	w.Header().Set("X-Source-SHA256", fmt.Sprintf("%x", sha256.Sum256([]byte(artifact.markdown))))
	w.Header().Set("X-PDF-Page-Count", strconv.Itoa(pages))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
