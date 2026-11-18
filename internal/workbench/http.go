package workbench

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"research-agent/internal/inference"
)

const projectsPath = "/api/v1/projects"

// NewHandler serves the API and files from a trusted web build directory.
// A nil model keeps saved conversations and research plans readable.
func NewHandler(store *Store, webFS fs.FS, model *inference.Client) http.Handler {
	mux := http.NewServeMux()
	conversation{store: store, model: model}.register(mux)
	researchTasks{store: store, model: model}.register(mux)
	experiments{store: store, model: model}.register(mux)
	store.registerMaterials(mux)
	mux.HandleFunc("GET "+projectsPath, store.handleListProjects)
	mux.HandleFunc("POST "+projectsPath, store.handleCreateProject)
	mux.HandleFunc(projectsPath, methodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc("GET "+projectsPath+"/{id}", store.handleGetProject)
	mux.HandleFunc(projectsPath+"/{id}", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/health", methodNotAllowed("GET, HEAD"))
	apiNotFound := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "API route not found")
	}
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/", apiNotFound)
	if webFS != nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				methodNotAllowed("GET, HEAD")(w, r)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" {
				name = "index.html"
			}
			if !fs.ValidPath(name) {
				http.NotFound(w, r)
				return
			}
			info, err := fs.Stat(webFS, name)
			if err != nil || !info.Mode().IsRegular() {
				http.NotFound(w, r)
				return
			}
			http.ServeFileFS(w, r, webFS, name)
		})
	}

	protection := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		if !isLoopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "INVALID_HOST", "Only loopback hosts are allowed")
			return
		}
		if err := protection.Check(r); err != nil {
			writeError(w, http.StatusForbidden, "CROSS_ORIGIN", "Cross-origin request denied")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func isLoopbackHost(host string) bool {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.EqualFold(host, "localhost") || net.ParseIP(strings.Trim(host, "[]")).IsLoopback()
}

func (s *Store) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var input createProjectInput
	if !readJSON(w, r, &input, 8<<10) {
		return
	}
	if !validRequestID(input.RequestID) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "requestId must be nonblank, at most 128 bytes, and contain no control characters")
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 200 || strings.ContainsRune(input.Title, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "title must contain 1 to 200 characters and no null characters")
		return
	}
	switch input.TemplateID {
	case "freeform", "literature_review", "paper", "experiment":
	default:
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "Unknown project template")
		return
	}

	project, created, err := s.createProject(r.Context(), input)
	if errors.Is(err, errRequestConflict) {
		writeError(w, http.StatusConflict, "REQUEST_ID_CONFLICT", errRequestConflict.Error())
		return
	}
	if err != nil {
		writeStoreError(w, "create project", err)
		return
	}
	if created {
		w.Header().Set("Location", projectsPath+"/"+project.ProjectID)
		writeJSON(w, http.StatusCreated, project)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Store) handleGetProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.getProject(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Project not found")
		return
	}
	if err != nil {
		writeStoreError(w, "get project", err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

type projectPage struct {
	Items      []Project `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

func (s *Store) handleListProjects(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid query parameters")
		return
	}
	limit := 50
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "limit must be an integer between 1 and 100")
			return
		}
	}
	cursor, ok := decodeProjectCursor(query.Get("cursor"))
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid project cursor")
		return
	}
	projects, err := s.listProjects(r.Context(), cursor, limit+1)
	if err != nil {
		writeStoreError(w, "list projects", err)
		return
	}
	page := projectPage{Items: projects}
	if len(projects) > limit {
		last := projects[limit-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt + "/" + last.ProjectID))
		page.Items = projects[:limit]
	}
	writeJSON(w, http.StatusOK, page)
}

func decodeProjectCursor(value string) (projectCursor, bool) {
	if value == "" {
		return projectCursor{}, true
	}
	if len(value) > 256 {
		return projectCursor{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return projectCursor{}, false
	}
	createdAt, id, ok := strings.Cut(string(data), "/")
	if !ok || id == "" {
		return projectCursor{}, false
	}
	_, err = time.Parse(createdAtLayout, createdAt)
	return projectCursor{createdAt: createdAt, projectID: id}, err == nil
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
	}
}

func validRequestID(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 128 && !strings.ContainsFunc(value, unicode.IsControl)
}

func readJSON(w http.ResponseWriter, r *http.Request, input any, limit int64) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		writeBodyError(w, err)
		return false
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		writeBodyError(w, err)
		return false
	}
	return true
}

func writeBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body is too large")
		return
	}
	writeError(w, http.StatusBadRequest, "INVALID_INPUT", "请提交一个 JSON 对象。")
}

func writeStoreError(w http.ResponseWriter, operation string, err error) {
	log.Printf("%s: %v", operation, err)
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not complete the request")
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Error apiError `json:"error"`
	}{Error: apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
