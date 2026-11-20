package workbench

import (
	"net/http"
	"strconv"
)

func (api projectDrafts) listMaterials(w http.ResponseWriter, r *http.Request) {
	cursor, err := eventCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "资料列表游标无效。")
		return
	}
	const pageSize = 50
	items, err := api.store.listDraftMaterials(r.Context(), r.PathValue("draftId"), cursor, pageSize+1)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	writeMaterialPage(w, items, pageSize)
}

func (api projectDrafts) uploadMaterial(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	revision, err := strconv.Atoi(query.Get("expectedRevision"))
	requestID := query.Get("requestId")
	if err != nil || revision < 1 || !validRequestID(requestID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提供有效的请求编号和草稿修订。")
		return
	}
	name, body := materialUpload(w, r)
	if body == nil {
		return
	}
	req := draftChangeInput{RequestID: requestID, ExpectedRevision: revision}
	draft, material, created, err := api.store.saveDraftMaterial(r.Context(), r.PathValue("draftId"), req, name, body)
	if writeMaterialTooLarge(w, err) {
		return
	}
	if err != nil {
		writeDraftError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, struct {
		Draft    projectDraft `json:"draft"`
		Material Material     `json:"material"`
	}{draft, material})
}

func (api projectDrafts) downloadMaterial(w http.ResponseWriter, r *http.Request) {
	material, err := api.store.draftMaterial(r.Context(), r.PathValue("draftId"), r.PathValue("materialId"))
	if err != nil {
		writeMaterialError(w, "read draft material", err)
		return
	}
	api.store.serveMaterial(w, r, material)
}
