package inference

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGeneratePNG(t *testing.T) {
	var fixture bytes.Buffer
	if err := png.Encode(&fixture, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"model":         "image-model",
		"prompt":        "绘制实验流程图",
		"n":             float64(1),
		"size":          "1536x1024",
		"quality":       "medium",
		"output_format": "png",
	}
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/deployment/v1/images/generations" ||
			r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("Accept") != "application/json" {
			t.Error("wrong image request method, path, or headers")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !reflect.DeepEqual(request, want) {
			t.Errorf("request = %#v, err = %v", request, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"url":%q}]}`, base64.StdEncoding.EncodeToString(fixture.Bytes()), "http://"+r.Host+"/output")
	}))
	defer upstream.Close()

	client, err := New(Config{BaseURL: upstream.URL + "/deployment/v1/", Model: "text-model", APIKey: "fixture", ReasoningEffort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.ForModel("image-model", "high").GeneratePNG(t.Context(), "绘制实验流程图", "1536x1024", "medium")
	if err != nil || !bytes.Equal(body, fixture.Bytes()) {
		t.Fatalf("decoded image = %x, err = %v", body, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
}

func TestGeneratePNGRejectsUnusableResponses(t *testing.T) {
	const private = "private-provider-detail"
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{name: "HTTP error", status: http.StatusServiceUnavailable, body: `{"error":"` + private + `"}`},
		{name: "redirect", status: http.StatusFound, body: private},
		{name: "URL only"},
		{name: "multiple images", body: `{"data":[{"b64_json":"YQ=="},{"b64_json":"Yg=="}]}`},
		{name: "extra null entry", body: `{"data":[{"b64_json":"YQ=="},null]}`},
		{name: "duplicate data field", body: `{"data":[{"b64_json":"YQ=="}],"data":[{"b64_json":"Yg=="}]}`},
		{name: "duplicate inline field", body: `{"data":[{"b64_json":"YQ==","b64_json":"Yg=="}]}`},
		{name: "missing data", body: `{}`},
		{name: "empty inline image", body: `{"data":[{"b64_json":""}]}`},
		{name: "invalid base64", body: `{"data":[{"b64_json":"` + private + `"}]}`},
		{name: "trailing content", body: `{"data":[{"b64_json":"YQ=="}]} ` + private},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/images/generations" {
					t.Error("unexpected request to provider output URL")
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if test.status != 0 {
					w.Header().Set("Location", "/output?secret="+private)
					w.WriteHeader(test.status)
				}
				if test.name == "URL only" {
					fmt.Fprintf(w, `{"data":[{"url":%q}]}`, "http://"+r.Host+"/output?secret="+private)
					return
				}
				io.WriteString(w, test.body)
			}))
			defer upstream.Close()

			client, err := New(Config{BaseURL: upstream.URL, Model: "image-model", APIKey: "fixture-key"})
			if err != nil {
				t.Fatal(err)
			}
			body, err := client.GeneratePNG(t.Context(), "prompt", "1024x1024", "low")
			if body != nil || err == nil || strings.Contains(err.Error(), private) || strings.Contains(err.Error(), "fixture-key") {
				t.Fatalf("decoded image = %x, err = %v", body, err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("provider requests = %d, want 1", got)
			}
		})
	}
}

func TestReadGeneratedPNGSizeLimit(t *testing.T) {
	const limit = 8 << 20
	const prefix = `{"data":[{"b64_json":"`
	const suffix = `"}]}`
	for _, size := range []int{limit, limit + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			inline := base64.StdEncoding.EncodeToString(make([]byte, size))
			body, err := readGeneratedPNG(io.MultiReader(strings.NewReader(prefix), strings.NewReader(inline), strings.NewReader(suffix)))
			if size == limit {
				if err != nil || len(body) != limit {
					t.Fatalf("decoded %d bytes at limit, err = %v", len(body), err)
				}
			} else if err == nil || body != nil {
				t.Fatalf("decoded %d bytes above limit, err = %v", len(body), err)
			}
		})
	}
	oversize := io.MultiReader(strings.NewReader(prefix+"YQ=="+suffix), strings.NewReader(strings.Repeat(" ", 12<<20)))
	if body, err := readGeneratedPNG(oversize); body != nil || err == nil {
		t.Fatalf("oversize response returned %d bytes, err = %v", len(body), err)
	}
}
