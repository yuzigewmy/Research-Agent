package workbench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"research-agent/internal/literature"
)

type fixtureLiteratureSource struct {
	work          literature.Work
	verifications int
}

func (s *fixtureLiteratureSource) Search(_ context.Context, _ string, _ int) ([]literature.Work, error) {
	return []literature.Work{s.work}, nil
}

func (s *fixtureLiteratureSource) Verify(_ context.Context, doi string) (literature.Work, error) {
	s.verifications++
	if doi != s.work.DOI {
		return literature.Work{}, literature.ErrNotFound
	}
	return s.work, nil
}

func TestLiteratureSavesOnlyVerifiedMetadata(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _, err := store.createProject(context.Background(), createProjectInput{RequestID: "references", Title: "河流研究", TemplateID: "literature_review"})
	if err != nil {
		t.Fatal(err)
	}
	const doi = "10.5555/stream.12"
	source := &fixtureLiteratureSource{work: literature.Work{DOI: doi, Title: "A & B results", Authors: []string{"Lin Chen"}, Year: 2024}}
	handler := NewHandler(store, nil, HandlerConfig{literature: source})
	base := "http://127.0.0.1" + projectsPath + "/" + project.ProjectID + "/literature"
	request := func(method, suffix, body string, want int) string {
		t.Helper()
		res := httptest.NewRecorder()
		req := httptest.NewRequest(method, base+suffix, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, suffix, res.Code, want, res.Body)
		}
		return res.Body.String()
	}
	request(http.MethodGet, "/search?q=stream", "", http.StatusOK)
	if body := request(http.MethodGet, "", "", http.StatusOK); body != "{\"items\":[]}\n" {
		t.Fatalf("search changed library: %s", body)
	}
	created := request(http.MethodPost, "", `{"doi":"https://doi.org/10.5555/STREAM.12"}`, http.StatusCreated)
	var citation Citation
	if err := json.Unmarshal([]byte(created), &citation); err != nil || citation.VerifiedAt == "" || citation.DOI != doi {
		t.Fatalf("invalid saved citation: %v, %s", err, created)
	}
	if replay := request(http.MethodPost, "", `{"doi":"10.5555/stream.12"}`, http.StatusOK); replay != created || source.verifications != 1 {
		t.Fatalf("replay changed the citation or verified it again: %s", replay)
	}
	bib := request(http.MethodGet, "/bibliography", "", http.StatusOK)
	if !strings.Contains(bib, `title = {A \& B results}`) || !strings.Contains(bib, "doi = {"+doi+"}") {
		t.Fatalf("incorrect bibliography: %s", bib)
	}
}
