package workbench

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWorkspaceComponentsReportConfigurationAndReadiness(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	check := func(assets fs.FS, config HandlerConfig) []workspaceComponent {
		t.Helper()
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1"+workspaceComponentCheckPath, nil)
		response := httptest.NewRecorder()
		NewHandler(store, assets, config).ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("component check returned %d: %s", response.Code, response.Body)
		}
		if (config.WordPython != "" && strings.Contains(response.Body.String(), filepath.Base(config.WordPython))) ||
			(config.SoftwarePDFFont != "" && strings.Contains(response.Body.String(), filepath.Base(config.SoftwarePDFFont))) {
			t.Fatal("component check exposed a deployment path")
		}
		var result workspaceComponentReport
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Components) != 3 {
			t.Fatalf("component response = %+v (%v)", result, err)
		}
		for index, id := range []string{"pdf.preview", "docx.export", "pdf.software.export"} {
			if component := result.Components[index]; component.ID != id || component.Message == "" {
				t.Fatalf("unexpected component record: %+v", component)
			}
		}
		return result.Components
	}

	statuses := func(components []workspaceComponent) []string {
		var values []string
		for _, component := range components {
			values = append(values, component.Status)
		}
		return values
	}
	if got := statuses(check(nil, HandlerConfig{})); !slices.Equal(got, []string{"unavailable", "unconfigured", "unconfigured"}) {
		t.Fatalf("unconfigured component states: %v", got)
	}

	assets := fstest.MapFS{
		"assets/pdf.worker-abc.mjs":                               {Data: []byte("worker")},
		"pdf-resources/version.json":                              {Data: []byte(`{"version":"6.0.0"}`)},
		"pdf-resources/cmaps/Adobe-GB1-UCS2.bcmap":                {Data: []byte("cmap")},
		"pdf-resources/iccs/CGATS001Compat-v2-micro.icc":          {Data: []byte("color")},
		"pdf-resources/standard_fonts/LiberationSans-Regular.ttf": {Data: []byte("font")},
		"pdf-resources/wasm/openjpeg.wasm":                        {Data: []byte("wasm")},
		"assets/index.js":                                         {Data: []byte("unrelated")},
	}
	missingDirectory := t.TempDir()
	missing := check(assets, HandlerConfig{
		WordPython:      filepath.Join(missingDirectory, "missing-interpreter.exe"),
		SoftwarePDFFont: filepath.Join(missingDirectory, "missing-font.ttf"),
	})
	if got := statuses(missing); !slices.Equal(got, []string{"ready", "unavailable", "unavailable"}) {
		t.Fatalf("missing component states: %v", got)
	}
	var pdfBytes int64
	for name, file := range assets {
		if name != "assets/index.js" {
			pdfBytes += int64(len(file.Data))
		}
	}
	if missing[0].Version != "6.0.0" || missing[0].Bytes != pdfBytes {
		t.Fatalf("PDF resource report did not match the files: %+v", missing[0])
	}
	t.Run("missing PDF resource", func(t *testing.T) {
		const requiredFile = "pdf-resources/wasm/openjpeg.wasm"
		file := assets[requiredFile]
		delete(assets, requiredFile)
		defer func() { assets[requiredFile] = file }()
		if got := check(assets, HandlerConfig{})[0]; got.Status != "unavailable" || got.Bytes != 0 {
			t.Fatalf("incomplete PDF resource bundle: %+v", got)
		}
	})

	t.Run("installed components", func(t *testing.T) {
		python, font := os.Getenv("WORDEXPORT_TEST_PYTHON"), os.Getenv("SOFTWARE_PDF_TEST_FONT")
		if python == "" || font == "" {
			t.Skip("configure the installed Word and PDF test components")
		}
		ready := check(assets, HandlerConfig{WordPython: python, SoftwarePDFFont: font})
		if got := statuses(ready); !slices.Equal(got, []string{"ready", "ready", "ready"}) {
			t.Fatalf("installed components were not usable: %+v", ready)
		}
		if ready[1].Version == "" || ready[2].Bytes <= 0 {
			t.Fatalf("versions or font size missing from installed components: %+v", ready)
		}
	})
}
