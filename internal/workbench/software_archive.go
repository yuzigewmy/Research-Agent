package workbench

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	softwareArchiveFormat = "archive"
	softwareArchiveBytes  = 16 << 20
	softwareArchiveItems  = 1000
)

var (
	errSoftwareArchiveInvalid = errors.New("压缩包结构或成员路径无效，请核对格式、重复路径和链接。")
	errSoftwareArchiveLimit   = errors.New("每个压缩包最多 1000 项，解压数据最多 16 MiB。")
	errSoftwareArchiveEmpty   = errors.New("压缩包中没有可用的源码或 Markdown、TXT 说明。")
	errSoftwareArchiveText    = errors.New("请将包内源码和说明保存为 UTF-8 文本。")
)

type softwareArchiveMember struct {
	name, digest string
	source       string
	preview      *MaterialPreview
	isSource     bool
}

type softwareArchiveScan struct {
	seen        map[string]struct{}
	members     []softwareArchiveMember
	sourceBytes int
	docs        int
}

func softwareArchiveKind(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "tar.gz"
	default:
		return ""
	}
}

func ignoredSoftwareArchivePath(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		switch strings.ToLower(part) {
		case ".git", ".svn", ".venv", "node_modules", "vendor", "__macosx", "__pycache__", "dist", "build", "target":
			return true
		}
	}
	return false
}

func (scan *softwareArchiveScan) inspect(ctx context.Context, name string, directory bool, size int64, open func() (io.ReadCloser, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if directory && (name == "." || name == "./") {
		return nil
	}
	name = strings.TrimPrefix(name, "./")
	if directory {
		name = strings.TrimSuffix(name, "/")
	}
	if !validWorkspacePath(name) {
		return errSoftwareArchiveInvalid
	}
	identity := strings.ToLower(name)
	if _, exists := scan.seen[identity]; exists {
		return errSoftwareArchiveInvalid
	}
	scan.seen[identity] = struct{}{}
	if directory || ignoredSoftwareArchivePath(name) {
		return nil
	}
	isSource := softwareSourcePath(name)
	extension := strings.ToLower(path.Ext(name))
	if !isSource && extension != ".md" && extension != ".markdown" && extension != ".txt" {
		return nil
	}
	if isSource && size > int64(maxSoftwareSourceBytes-scan.sourceBytes) {
		return errSoftwareSources
	}
	reader, err := open()
	if err != nil {
		return errSoftwareArchiveInvalid
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, size+1))
	if err != nil || int64(len(content)) != size {
		return errSoftwareArchiveInvalid
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return errSoftwareArchiveText
	}
	member := softwareArchiveMember{name: name, digest: fmt.Sprintf("%x", sha256.Sum256(content)), isSource: isSource}
	if isSource {
		scan.sourceBytes += len(content)
		member.source = string(content)
	} else if scan.docs < maxTurnReferences {
		preview, err := previewText(bytes.NewReader(content), referenceExcerptBytes)
		if err != nil {
			return err
		}
		member.preview = &preview
		scan.docs++
	}
	scan.members = append(scan.members, member)
	return nil
}

func (scan *softwareArchiveScan) readZIP(ctx context.Context, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return errSoftwareArchiveInvalid
	}
	if len(reader.File) > softwareArchiveItems {
		return errSoftwareArchiveLimit
	}
	remaining := uint64(softwareArchiveBytes)
	for _, entry := range reader.File {
		mode := entry.Mode().Type()
		if mode != 0 && mode != os.ModeDir {
			return errSoftwareArchiveInvalid
		}
		if entry.UncompressedSize64 > remaining {
			return errSoftwareArchiveLimit
		}
		remaining -= entry.UncompressedSize64
		if err := scan.inspect(ctx, entry.Name, mode.IsDir(), int64(entry.UncompressedSize64), entry.Open); err != nil {
			return err
		}
	}
	return nil
}

func (scan *softwareArchiveScan) readTarGzip(ctx context.Context, file *os.File) error {
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return errSoftwareArchiveInvalid
	}
	defer gzipReader.Close()
	expanded := &io.LimitedReader{R: gzipReader, N: softwareArchiveBytes + 1}
	reader := tar.NewReader(expanded)
	for count := 0; ; count++ {
		entry, err := reader.Next()
		if expanded.N == 0 {
			return errSoftwareArchiveLimit
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || entry.Size < 0 {
			return errSoftwareArchiveInvalid
		}
		if count >= softwareArchiveItems || entry.Size >= expanded.N {
			return errSoftwareArchiveLimit
		}
		if entry.Typeflag != tar.TypeDir && entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA {
			return errSoftwareArchiveInvalid
		}
		if err := scan.inspect(ctx, entry.Name, entry.Typeflag == tar.TypeDir, entry.Size,
			func() (io.ReadCloser, error) { return io.NopCloser(reader), nil }); err != nil {
			return err
		}
	}
	// Consume gzip's footer so a damaged checksum cannot pass as healthy input.
	_, err = io.Copy(io.Discard, expanded)
	if expanded.N == 0 {
		return errSoftwareArchiveLimit
	}
	if err != nil {
		return errSoftwareArchiveInvalid
	}
	return ctx.Err()
}

func (s *Store) readSoftwareArchive(ctx context.Context, material TurnReference) ([]softwareArchiveMember, error) {
	file, err := s.openObject(material.SHA256)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scan := softwareArchiveScan{seen: make(map[string]struct{})}
	if softwareArchiveKind(material.Name) == "zip" {
		err = scan.readZIP(ctx, file)
	} else {
		err = scan.readTarGzip(ctx, file)
	}
	if err == nil && len(scan.members) == 0 {
		err = errSoftwareArchiveEmpty
	}
	if err != nil {
		return nil, workflowOutputError{err}
	}
	return scan.members, nil
}
