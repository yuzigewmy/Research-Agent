package literature

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client := New()
	client.baseURL = server.URL
	client.httpClient.Transport = server.Client().Transport
	return client
}

func TestSearchDeduplicatesDOIsAndRetainsRankedMetadata(t *testing.T) {
	const query = "光谱 A&B + C"
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "No acceptable response type", http.StatusNotAcceptable)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/works" || r.URL.Query().Get("query") != query || r.URL.Query().Get("rows") != "5" {
			t.Errorf("unexpected search request: %s %s", r.Method, r.RequestURI)
		}
		io.WriteString(w, `{"status":"ok","message-type":"work-list","message":{"items":[
			{"DOI":"10.54321/Optics.A","title":["","光谱研究"],"container-title":["Spectral Notes"],
			 "published":{"date-parts":[[2024,3]]},"issued":{"date-parts":[[2023]]},"language":"zh",
			 "author":[{"given":"Amira","family":"Chen"},{"name":"Spectra Group"},{}],
			 "abstract":"<jats:p>First &amp; second.</jats:p><jats:p>第三项</jats:p>","URL":"https://example.invalid/target"},
			{"DOI":" https://doi.org/10.54321/OPTICS.A ","title":["duplicate"]},
			{"DOI":"invalid","title":["ignored"]},
			{"DOI":"10.54321/Optics.B","title":["Follow-up"],"issued":{"date-parts":[[2022]]}}
		]}}`)
	})
	works, err := client.Search(context.Background(), " "+query+" ", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []Work{
		{DOI: "10.54321/optics.a", Title: "光谱研究", Journal: "Spectral Notes", Year: 2024, Language: "zh", Authors: []string{"Amira Chen", "Spectra Group"}, Abstract: "First & second. 第三项", URL: "https://doi.org/10.54321/optics.a"},
		{DOI: "10.54321/optics.b", Title: "Follow-up", Year: 2022, URL: "https://doi.org/10.54321/optics.b"},
	}
	if !reflect.DeepEqual(works, want) {
		t.Fatalf("works = %#v, want %#v", works, want)
	}
}

func TestVerifyChecksEscapedDOIAndBoundsMetadata(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/works/10.54321%2Fstudy%2Fnote%3Fv%23rev" || r.URL.RawQuery != "" {
			t.Errorf("unexpected verification request: %s %s", r.Method, r.RequestURI)
		}
		authors := make([]map[string]string, 80)
		for i := range authors {
			authors[i] = map[string]string{"name": fmt.Sprintf("Contributor %d", i+1)}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "message-type": "work", "message": map[string]any{
				"DOI": "10.54321/Study/Note?V#Rev", "title": []string{"An observed signal"},
				"author": authors, "abstract": strings.Repeat("文", 5000),
				"URL":              "https://example.invalid/served-elsewhere",
				"published-online": map[string]any{"date-parts": [][]int{{2021, 1}}},
			},
		})
	})
	work, err := client.Verify(context.Background(), " HTTPS://DX.DOI.ORG/10.54321/Study/Note%3FV%23Rev ")
	if err != nil {
		t.Fatal(err)
	}
	if work.DOI != "10.54321/study/note?v#rev" || work.Title != "An observed signal" || work.Year != 2021 || work.URL != "https://doi.org/10.54321/study/note%3Fv%23rev" {
		t.Fatalf("unexpected verification metadata: %+v", work)
	}
	if len(work.Authors) != 30 || work.Authors[0] != "Contributor 1" || work.Authors[29] != "Contributor 30" || utf8.RuneCountInString(work.Abstract) != 4000 {
		t.Fatalf("unexpected metadata limits: %d authors, %d abstract characters", len(work.Authors), utf8.RuneCountInString(work.Abstract))
	}
}

func TestVerifyRejectsInvalidDOIBeforeRequesting(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid DOI reached server: %s", r.RequestURI)
	})
	for _, doi := range []string{
		"https://example.invalid/10.54321/work", "https://doi.org@elsewhere.invalid/10.54321/work",
		"https://doi.org/10.54321/work?x=1", "https://doi.org/10.54321/%2E%2E/works",
		"10.54321/../works", `10.54321/work\name`, "10.54321/", "10.54321/work name",
	} {
		if _, err := client.Verify(context.Background(), doi); !errors.Is(err, ErrInvalidDOI) {
			t.Errorf("Verify(%q) error = %v, want ErrInvalidDOI", doi, err)
		}
		if got, err := NormalizeDOI(doi); got != "" || !errors.Is(err, ErrInvalidDOI) {
			t.Errorf("NormalizeDOI(%q) = %q, %v, want ErrInvalidDOI", doi, got, err)
		}
	}
}

func TestNormalizeDOIAcceptsCommonForms(t *testing.T) {
	for _, input := range []string{" 10.54321/STUDY ", "DoI: 10.54321/Study", "http://dx.doi.org/10.54321%2FStudy"} {
		if doi, err := NormalizeDOI(input); doi != "10.54321/study" || err != nil {
			t.Errorf("NormalizeDOI(%q) = %q, %v", input, doi, err)
		}
	}
}

func TestSearchAcceptsRequestedRange(t *testing.T) {
	for _, rows := range []string{"1", "80"} {
		t.Run(rows, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("rows") != rows {
					t.Errorf("unexpected rows: %s", r.URL.RawQuery)
				}
				io.WriteString(w, `{"status":"ok","message-type":"work-list","message":{"items":[]}}`)
			})
			limit := 1
			if rows == "80" {
				limit = 80
			}
			works, err := client.Search(context.Background(), "spectra", limit)
			if err != nil || works == nil || len(works) != 0 {
				t.Errorf("empty search = %v, %v", works, err)
			}
			for _, invalid := range []int{0, 81} {
				if _, err := client.Search(context.Background(), "spectra", invalid); !errors.Is(err, ErrInvalidQuery) {
					t.Errorf("Search(..., %d) error = %v, want ErrInvalidQuery", invalid, err)
				}
			}
		})
	}
}

func TestLookupTimeoutAndContextCancellation(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	client.httpClient.Timeout = 50 * time.Millisecond
	if _, err := client.Verify(context.Background(), "10.54321/pending"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("client timeout error = %v, want ErrUnavailable", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Verify(ctx, "10.54321/pending"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context error = %v, want context.Canceled", err)
	}
}

func TestVerifySeparatesNotFoundFromUpstreamFailure(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/works/10.54321/") {
		case "missing":
			http.NotFound(w, r)
		case "busy":
			w.WriteHeader(http.StatusTooManyRequests)
		case "oversized":
			io.WriteString(w, strings.Repeat(" ", 5<<20))
		case "mismatch":
			io.WriteString(w, `{"status":"ok","message-type":"work","message":{"DOI":"10.54321/other"}}`)
		case "redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
		default:
			t.Errorf("unexpected request: %s", r.RequestURI)
		}
	})
	for _, test := range []struct {
		suffix string
		want   error
	}{
		{"missing", ErrNotFound},
		{"busy", ErrUnavailable},
		{"oversized", ErrUnavailable},
		{"mismatch", ErrUnavailable},
		{"redirect", ErrUnavailable},
	} {
		t.Run(test.suffix, func(t *testing.T) {
			if _, err := client.Verify(context.Background(), "doi:10.54321/"+test.suffix); !errors.Is(err, test.want) {
				t.Errorf("Verify error = %v, want %v", err, test.want)
			}
		})
	}
}
