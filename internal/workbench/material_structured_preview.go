package workbench

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	maxStructuredArchiveEntries = 100
	maxStructuredArchiveScan    = 5000
	maxStructuredCodeLines      = 200
)

var errStructuredPreviewUnsupported = errors.New("material does not support structured preview")

type ArchivePreviewEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
	Warning string `json:"warning,omitempty"`
}

type CodePreviewLine struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type MaterialStructuredPreview struct {
	Kind         string                `json:"kind"`
	Format       string                `json:"format,omitempty"`
	Language     string                `json:"language,omitempty"`
	TotalEntries int                   `json:"totalEntries,omitempty"`
	UnsafeCount  int                   `json:"unsafeCount,omitempty"`
	Entries      []ArchivePreviewEntry `json:"entries,omitempty"`
	TotalLines   int                   `json:"totalLines,omitempty"`
	Lines        []CodePreviewLine     `json:"lines,omitempty"`
	Truncated    bool                  `json:"truncated"`
}

func structuredCodeLanguage(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".tex":
		return "latex"
	case ".bib":
		return "bibtex"
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js":
		return "javascript"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return ""
	}
}

func inspectArchiveEntryPath(raw string, isDir bool) (cleanPath string, blocked bool, warning string) {
	if raw == "" {
		return "(空路径)", true, "已拦截：成员路径为空"
	}
	if strings.ContainsFunc(raw, unicode.IsControl) {
		return raw, true, "已拦截：成员路径包含控制字符"
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") || strings.ContainsAny(raw, `\:`) {
		return raw, true, "已拦截：存在绝对路径或非法分隔符（疑似路径穿越）"
	}
	normalized := strings.TrimPrefix(raw, "./")
	if isDir {
		normalized = strings.TrimSuffix(normalized, "/")
	}
	if normalized == "" || normalized == "." {
		return ".", false, ""
	}
	for part := range strings.SplitSeq(normalized, "/") {
		if part == ".." {
			return raw, true, "已拦截：存在上级目录穿越（..）"
		}
	}
	if !validWorkspacePath(normalized) {
		return raw, true, "已拦截：成员路径不符合安全相对路径规范"
	}
	return normalized, false, ""
}

func previewZIPStructured(file *os.File) (MaterialStructuredPreview, error) {
	info, err := file.Stat()
	if err != nil {
		return MaterialStructuredPreview{}, err
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return MaterialStructuredPreview{}, errStructuredPreviewUnsupported
	}
	result := MaterialStructuredPreview{
		Kind:         "archive",
		Format:       "zip",
		TotalEntries: len(reader.File),
		Entries:      make([]ArchivePreviewEntry, 0, min(len(reader.File), maxStructuredArchiveEntries)),
	}
	for _, entry := range reader.File {
		mode := entry.Mode().Type()
		isDir := mode.IsDir() || strings.HasSuffix(entry.Name, "/")
		cleanPath, blocked, warning := inspectArchiveEntryPath(entry.Name, isDir)
		if !blocked && mode != 0 && mode != os.ModeDir {
			blocked = true
			warning = "已拦截：不支持符号链接或特殊文件条目"
		}
		if blocked {
			result.UnsafeCount++
		}
		if len(result.Entries) < maxStructuredArchiveEntries {
			result.Entries = append(result.Entries, ArchivePreviewEntry{
				Path:    cleanPath,
				Size:    int64(entry.UncompressedSize64),
				IsDir:   isDir,
				Blocked: blocked,
				Warning: warning,
			})
		} else {
			result.Truncated = true
		}
	}
	return result, nil
}

func previewTarGzipStructured(file *os.File) (MaterialStructuredPreview, error) {
	gz, err := gzip.NewReader(file)
	if err != nil {
		return MaterialStructuredPreview{}, errStructuredPreviewUnsupported
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	result := MaterialStructuredPreview{
		Kind:    "archive",
		Format:  "tar.gz",
		Entries: make([]ArchivePreviewEntry, 0, maxStructuredArchiveEntries),
	}
	for scanned := 0; scanned < maxStructuredArchiveScan; scanned++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return MaterialStructuredPreview{}, errStructuredPreviewUnsupported
		}
		result.TotalEntries++
		isDir := header.Typeflag == tar.TypeDir
		cleanPath, blocked, warning := inspectArchiveEntryPath(header.Name, isDir)
		if !blocked && header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			blocked = true
			warning = "已拦截：不支持符号链接、硬链接或特殊设备条目"
		}
		if blocked {
			result.UnsafeCount++
		}
		if len(result.Entries) < maxStructuredArchiveEntries {
			result.Entries = append(result.Entries, ArchivePreviewEntry{
				Path:    cleanPath,
				Size:    header.Size,
				IsDir:   isDir,
				Blocked: blocked,
				Warning: warning,
			})
		} else {
			result.Truncated = true
		}
	}
	return result, nil
}

func (s *Store) previewMaterialStructured(material Material) (MaterialStructuredPreview, error) {
	if archiveKind := softwareArchiveKind(material.Name); archiveKind != "" {
		file, err := s.openObject(material.SHA256)
		if err != nil {
			return MaterialStructuredPreview{}, err
		}
		defer file.Close()
		if archiveKind == "zip" {
			return previewZIPStructured(file)
		}
		return previewTarGzipStructured(file)
	}
	lang := structuredCodeLanguage(material.Name)
	if lang == "" {
		return MaterialStructuredPreview{}, errStructuredPreviewUnsupported
	}
	excerpt, err := s.previewObject(material.SHA256, materialPreviewBytes)
	if errors.Is(err, errMaterialNotText) {
		return MaterialStructuredPreview{}, errStructuredPreviewUnsupported
	}
	if err != nil {
		return MaterialStructuredPreview{}, err
	}
	normalized := strings.ReplaceAll(excerpt.Text, "\r\n", "\n")
	rawLines := strings.Split(normalized, "\n")
	if len(rawLines) == 1 && rawLines[0] == "" {
		rawLines = nil
	}
	truncated := excerpt.Truncated
	totalLines := len(rawLines)
	if len(rawLines) > maxStructuredCodeLines {
		rawLines = rawLines[:maxStructuredCodeLines]
		truncated = true
	}
	lines := make([]CodePreviewLine, len(rawLines))
	for i, line := range rawLines {
		lines[i] = CodePreviewLine{Number: i + 1, Text: line}
	}
	return MaterialStructuredPreview{
		Kind:       "code",
		Language:   lang,
		TotalLines: totalLines,
		Lines:      lines,
		Truncated:  truncated,
	}, nil
}

func (s *Store) handleStructuredPreviewMaterial(w http.ResponseWriter, r *http.Request) {
	material, err := s.getMaterial(r.Context(), r.PathValue("id"), r.PathValue("materialId"))
	if err != nil {
		writeMaterialError(w, "read material for structured preview", err)
		return
	}
	preview, err := s.previewMaterialStructured(material)
	if errors.Is(err, errStructuredPreviewUnsupported) {
		writeError(w, http.StatusUnprocessableEntity, "PREVIEW_UNAVAILABLE", "仅支持有效压缩包（.zip/.tar.gz/.tgz）及 UTF-8 代码/LaTeX 模板文件的结构化预览。")
		return
	}
	if err != nil {
		writeStoreError(w, "structured preview material", err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
