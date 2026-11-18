package workbench

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"research-agent/internal/literature"
)

type literatureSource interface {
	Search(context.Context, string, int) ([]literature.Work, error)
	Verify(context.Context, string) (literature.Work, error)
}

type literatureLibrary struct {
	store  *Store
	source literatureSource
}

func (api literatureLibrary) register(mux *http.ServeMux) {
	base := projectsPath + "/{id}/literature"
	mux.HandleFunc("GET "+base, api.list)
	mux.HandleFunc("POST "+base, api.add)
	mux.HandleFunc("GET "+base+"/search", api.search)
	mux.HandleFunc("GET "+base+"/bibliography", api.export)
	mux.HandleFunc(base, methodNotAllowed("GET, HEAD, POST"))
}

func (api literatureLibrary) list(w http.ResponseWriter, r *http.Request) {
	before, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "文献列表游标无效。")
		return
	}
	if !api.projectExists(w, r) {
		return
	}
	const pageSize = 50
	items, err := api.store.listCitations(r.Context(), r.PathValue("id"), before, pageSize+1)
	if err != nil {
		writeStoreError(w, "list verified literature", err)
		return
	}
	var next string
	if len(items) > pageSize {
		next = strconv.FormatInt(items[pageSize-1].ordinal, 10)
		items = items[:pageSize]
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []Citation `json:"items"`
		NextCursor string     `json:"nextCursor,omitempty"`
	}{items, next})
}

func (api literatureLibrary) search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	text := strings.TrimSpace(query.Get("q"))
	if text == "" || len(text) > 512 {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请输入不超过 512 字节的检索词。")
		return
	}
	limit := 20
	if value := query.Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 80 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "检索数量须为 1 至 80。")
			return
		}
	}
	if !api.projectExists(w, r) {
		return
	}
	items, err := api.source.Search(r.Context(), text, limit)
	if err != nil {
		writeLiteratureError(w, err)
		return
	}
	if items == nil {
		items = []literature.Work{}
	}
	writeJSON(w, http.StatusOK, struct {
		Items []literature.Work `json:"items"`
	}{items})
}

func (api literatureLibrary) add(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DOI string `json:"doi"`
	}
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	doi, err := literature.NormalizeDOI(input.DOI)
	if err != nil {
		writeLiteratureError(w, err)
		return
	}
	if !api.projectExists(w, r) {
		return
	}
	ctx, projectID := r.Context(), r.PathValue("id")
	citation, err := api.store.getCitation(ctx, projectID, doi)
	if err == nil {
		writeJSON(w, http.StatusOK, citation)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeStoreError(w, "look up verified literature", err)
		return
	}
	work, err := api.source.Verify(ctx, doi)
	if err != nil {
		writeLiteratureError(w, err)
		return
	}
	citation, created, err := api.store.saveCitation(ctx, projectID, work)
	if err != nil {
		writeStoreError(w, "save verified literature", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, citation)
}

func (api literatureLibrary) export(w http.ResponseWriter, r *http.Request) {
	if !api.projectExists(w, r) {
		return
	}
	items, err := api.store.listCitations(r.Context(), r.PathValue("id"), 0, -1)
	if err != nil {
		writeStoreError(w, "read bibliography", err)
		return
	}
	w.Header().Set("Content-Type", "application/x-bibtex; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="references.bib"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "references.bib", time.Time{}, strings.NewReader(bibliography(items)))
}

func (api literatureLibrary) projectExists(w http.ResponseWriter, r *http.Request) bool {
	_, err := api.store.getProject(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "项目不存在。")
	} else if err != nil {
		writeStoreError(w, "look up literature project", err)
	}
	return err == nil
}

func writeLiteratureError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, literature.ErrInvalidDOI):
		writeError(w, http.StatusBadRequest, "INVALID_DOI", "请输入有效的 DOI 或 DOI 链接。")
	case errors.Is(err, literature.ErrNotFound):
		writeError(w, http.StatusNotFound, "DOI_NOT_FOUND", "Crossref 未找到此 DOI 的登记记录。")
	default:
		writeError(w, http.StatusBadGateway, "LITERATURE_UNAVAILABLE", "暂时无法从 Crossref 读取记录，请稍后重试。")
	}
}
