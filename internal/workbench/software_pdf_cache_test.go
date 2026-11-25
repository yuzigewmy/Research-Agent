package workbench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSoftwarePDFCacheRepairsRecordAtCapacity(t *testing.T) {
	ctx := t.Context()
	store := &Store{objectsDir: filepath.Join(t.TempDir(), "objects")}
	root, err := store.openSoftwarePDFCache(true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	for i := range maxSoftwarePDFCacheFiles {
		name := fmt.Sprintf("%064x", i) + softwarePDFCacheSuffix
		if err := root.WriteFile(name, []byte("invalid record"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	name := strings.Repeat("0", 64) + softwarePDFCacheSuffix
	if body, _ := store.findCachedSoftwarePDF(ctx, name); body != nil {
		t.Fatal("cache read accepted a corrupt record")
	}
	body := []byte("%PDF-1.7\nfixture\n")
	if err := store.saveCachedSoftwarePDF(ctx, name, body, 2); err != nil {
		t.Fatal(err)
	}
	if got, pages := store.findCachedSoftwarePDF(ctx, name); !bytes.Equal(got, body) || pages != 2 {
		t.Fatal("same-key replacement at the file limit did not repair the record")
	}
	if err := store.saveCachedSoftwarePDF(ctx, strings.Repeat("f", 64)+softwarePDFCacheSuffix, body, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := store.softwarePDFCacheUsage(ctx, false); err != nil || got.count != maxSoftwarePDFCacheFiles {
		t.Fatalf("write exceeded the cache capacity: %+v (%v)", got, err)
	}

	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.softwarePDFCacheUsage(stopped, true); !errors.Is(err, context.Canceled) || store.softwarePDFCache.epoch != 0 {
		t.Fatalf("canceled clear advanced the cache epoch: %v", err)
	}
}

func TestWorkspaceCacheFailureKeepsBaseStorageUsage(t *testing.T) {
	workspace := t.TempDir()
	store, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cachePath := filepath.Join(workspace, softwarePDFCacheDirectory)
	const contents = "file occupying the cache path"
	if err := os.WriteFile(cachePath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, err := store.workspaceStorage(t.Context())
	if err != nil || !usage.CacheUnavailable || usage.DatabaseBytes == 0 || usage.TotalBytes != usage.DatabaseBytes+usage.ObjectBytes {
		t.Fatalf("cache directory failure hid base workspace usage: %+v (%v)", usage, err)
	}
	if _, err := store.softwarePDFCacheUsage(t.Context(), true); err == nil {
		t.Fatal("clear accepted a regular file in place of the cache directory")
	}
	if got, err := os.ReadFile(cachePath); err != nil || string(got) != contents {
		t.Fatalf("clear changed the file occupying the cache path: %q (%v)", got, err)
	}
}

func TestWorkspaceCacheRefusesLinkedDirectory(t *testing.T) {
	workspace := t.TempDir()
	store, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sibling := filepath.Join(workspace, "preserved")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	preserved := filepath.Join(sibling, strings.Repeat("a", 64)+softwarePDFCacheSuffix)
	const contents = "preserved file"
	if err := os.WriteFile(preserved, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("preserved", filepath.Join(workspace, softwarePDFCacheDirectory)); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := store.softwarePDFCacheUsage(t.Context(), true); err == nil {
		t.Fatal("clear accepted a linked cache directory")
	}
	if got, err := os.ReadFile(preserved); err != nil || string(got) != contents {
		t.Fatalf("clear changed a file outside the cache directory: %q (%v)", got, err)
	}

	res := httptest.NewRecorder()
	NewHandler(store, nil, HandlerConfig{}).ServeHTTP(res, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1/api/v1/workspace-storage", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"cacheUnavailable":true`) {
		t.Fatalf("cache directory failure hid base workspace usage: %d %s", res.Code, res.Body)
	}
}
