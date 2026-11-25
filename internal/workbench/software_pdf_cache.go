package workbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	softwarePDFCacheDirectory = "software-pdf-cache"
	softwarePDFCacheSuffix    = ".pdf-cache"
	softwarePDFLayoutRevision = "a4-v1"
	maxCachedPDFBytes         = 16 << 20
	maxSoftwarePDFCacheBytes  = 128 << 20
	maxSoftwarePDFCacheFiles  = 256
	cachedPDFHeaderSize       = 4 + sha256.Size
)

type softwarePDFCache struct {
	mu    sync.Mutex
	epoch uint64
}

type softwarePDFSource struct {
	projectID, artifactID, title, version, kind, text string
}

func (src softwarePDFSource) cacheKey(font []byte) string {
	parts, _ := json.Marshal([]string{
		softwarePDFLayoutRevision, softwarePDFEngineIdentity, src.projectID, src.artifactID, src.kind,
		src.title, src.version, pdfCacheDigest([]byte(src.text)), pdfCacheDigest(font),
	})
	return pdfCacheDigest(parts)
}

func pdfCacheDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (s *Store) cachedSoftwarePDF(ctx context.Context, font []byte, src softwarePDFSource) ([]byte, int, bool, error) {
	name := src.cacheKey(font) + softwarePDFCacheSuffix
	cache := &s.softwarePDFCache
	cache.mu.Lock()
	epoch := cache.epoch
	body, pages := s.findCachedSoftwarePDF(ctx, name)
	cache.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	if body != nil {
		return body, pages, true, nil
	}
	body, pages, err := renderSoftwareArtifactPDF(ctx, font, src.title, src.version, src.kind, src.text)
	if ctx.Err() != nil {
		return nil, 0, false, ctx.Err()
	}
	if err != nil || len(body) > maxCachedPDFBytes {
		return body, pages, false, err
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	// A clear operation invalidates writes from renders already in progress.
	if cache.epoch != epoch {
		return body, pages, false, nil
	}
	existing, existingPages := s.findCachedSoftwarePDF(ctx, name)
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	if existing != nil {
		return existing, existingPages, true, nil
	}
	err = s.saveCachedSoftwarePDF(ctx, name, body, pages)
	if stopped := ctx.Err(); stopped != nil {
		return nil, 0, false, stopped
	}
	if err != nil {
		slog.Debug("software PDF cache write skipped", "error", err)
	}
	return body, pages, false, nil
}

func (s *Store) openSoftwarePDFCache(create bool) (*os.Root, error) {
	workspace, err := os.OpenRoot(filepath.Dir(s.objectsDir))
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	if create {
		if err := workspace.Mkdir(softwarePDFCacheDirectory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	info, err := workspace.Lstat(softwarePDFCacheDirectory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("PDF cache path is not a directory")
	}
	root, err := workspace.OpenRoot(softwarePDFCacheDirectory)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("PDF cache directory changed while opening")
	}
	return root, nil
}

func (s *Store) findCachedSoftwarePDF(ctx context.Context, name string) ([]byte, int) {
	root, err := s.openSoftwarePDFCache(false)
	if err != nil {
		return nil, 0
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCachedPDFBytes+cachedPDFHeaderSize {
		return nil, 0
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, 0
	}
	defer file.Close()
	record, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxCachedPDFBytes+cachedPDFHeaderSize+1))
	if err != nil || len(record) <= cachedPDFHeaderSize || len(record) > maxCachedPDFBytes+cachedPDFHeaderSize {
		return nil, 0
	}
	pages := int(binary.BigEndian.Uint32(record[:4]))
	body := record[cachedPDFHeaderSize:]
	if pages <= 0 || !bytes.HasPrefix(body, []byte("%PDF-")) || !bytes.Equal(record[4:cachedPDFHeaderSize], cachedPDFDigest(name, record[:4], body)) {
		return nil, 0
	}
	return body, pages
}

func cachedPDFDigest(name string, pages, body []byte) []byte {
	hash := sha256.New()
	_, _ = io.WriteString(hash, name)
	_, _ = hash.Write(pages)
	_, _ = hash.Write(body)
	return hash.Sum(nil)
}

func (s *Store) saveCachedSoftwarePDF(ctx context.Context, name string, body []byte, pages int) error {
	root, err := s.openSoftwarePDFCache(true)
	if err != nil {
		return err
	}
	defer root.Close()
	usage, err := scanPDFCache(ctx, root, false)
	if err != nil {
		return err
	}
	if existing, err := root.Lstat(name); err == nil {
		if !existing.Mode().IsRegular() {
			return errors.New("PDF cache record is not a regular file")
		}
		// A corrupt record can be replaced even when the cache is full.
		usage.count--
		usage.bytes -= existing.Size()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if usage.count >= maxSoftwarePDFCacheFiles || usage.bytes+int64(len(body)+cachedPDFHeaderSize) > maxSoftwarePDFCacheBytes {
		return nil
	}
	staging := ".pending-" + pdfCacheDigest([]byte(rand.Text())) + softwarePDFCacheSuffix
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(staging)
	var header [cachedPDFHeaderSize]byte
	binary.BigEndian.PutUint32(header[:4], uint32(pages))
	copy(header[4:], cachedPDFDigest(name, header[:4], body))
	source := io.MultiReader(bytes.NewReader(header[:]), bytes.NewReader(body))
	_, writeErr := io.Copy(file, contextReader{ctx, source})
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return root.Rename(staging, name)
}

type pdfCacheUsage struct {
	bytes int64
	count int
}

// Both accounting and cleanup operate only on managed regular cache files.
func scanPDFCache(ctx context.Context, root *os.Root, remove bool) (pdfCacheUsage, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return pdfCacheUsage{}, err
	}
	var usage pdfCacheUsage
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return usage, err
		}
		name := entry.Name()
		key, ok := strings.CutSuffix(strings.TrimPrefix(name, ".pending-"), softwarePDFCacheSuffix)
		if !ok || !validObjectDigest(key) || !entry.Type().IsRegular() {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			return usage, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if remove {
			if err := root.Remove(name); err != nil {
				return usage, err
			}
		}
		usage.count++
		usage.bytes += info.Size()
	}
	return usage, nil
}

func (s *Store) softwarePDFCacheUsage(ctx context.Context, clear bool) (pdfCacheUsage, error) {
	cache := &s.softwarePDFCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return pdfCacheUsage{}, err
	}
	if clear {
		cache.epoch++
	}
	root, err := s.openSoftwarePDFCache(false)
	if errors.Is(err, os.ErrNotExist) {
		return pdfCacheUsage{}, nil
	}
	if err != nil {
		return pdfCacheUsage{}, err
	}
	defer root.Close()
	return scanPDFCache(ctx, root, clear)
}

func (s *Store) handleWorkspaceCacheClear(w http.ResponseWriter, r *http.Request) {
	removed, err := s.softwarePDFCacheUsage(r.Context(), true)
	if err != nil {
		slog.Warn("software PDF cache clear failed", "error", err, "removed_count", removed.count)
		writeError(w, http.StatusInternalServerError, "CACHE_CLEAR_FAILED", "清理未完成。请刷新用量后重试。")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		RemovedBytes int64 `json:"removedBytes"`
		RemovedCount int   `json:"removedCount"`
	}{removed.bytes, removed.count})
}
