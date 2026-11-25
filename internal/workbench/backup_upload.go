package workbench

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxRestoreUploadBytes   = 1 << 30
	maxRestoreDatabaseBytes = 64 << 20
	maxBackupDirectory      = 32 << 20
	restoreUploadTimeout    = 5 * time.Minute
)

func (s *Store) handleWorkspaceRestore(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/zip" {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "请选择工作台导出的 ZIP 备份。")
		return
	}
	if r.ContentLength > maxRestoreUploadBytes {
		writeRestoreTooLarge(w)
		return
	}
	if !s.backupMu.TryLock() {
		writeError(w, http.StatusConflict, "WORKSPACE_TRANSFER_BUSY", "正在处理另一项备份或恢复，请稍后重试。")
		return
	}
	defer s.backupMu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), restoreUploadTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeRestoreStorageError(w, err)
		return
	}
	root := filepath.Dir(s.objectsDir)
	upload, err := os.CreateTemp(root, ".research-restore-upload-")
	if err != nil {
		writeRestoreStorageError(w, err)
		return
	}
	defer os.Remove(upload.Name())
	defer upload.Close()

	body := http.MaxBytesReader(w, r.Body, maxRestoreUploadBytes)
	size, err := io.Copy(upload, backupReader{ctx, body})
	if err != nil {
		var tooLarge *http.MaxBytesError
		var fileError *os.PathError
		switch {
		case errors.As(err, &tooLarge):
			writeRestoreTooLarge(w)
		case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
			writeRestoreTimeout(w)
		case errors.As(err, &fileError):
			writeRestoreStorageError(w, err)
		default:
			writeError(w, http.StatusBadRequest, "RESTORE_UPLOAD_FAILED", "上传中断。请重新选择备份并重试。")
		}
		return
	}
	source := restoreReaderAt{ctx, upload}
	destination := filepath.Join(root, "restored-"+strings.ToLower(rand.Text()))
	err = validateRestoreDirectory(source, size)
	if err == nil {
		err = restoreWorkspace(ctx, upload, size, destination, maxRestoreDatabaseBytes)
	}
	if err != nil {
		var fileError *os.PathError
		var renameError *os.LinkError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
			writeRestoreTimeout(w)
		case errors.Is(err, errRestoreDatabaseSize):
			writeError(w, http.StatusRequestEntityTooLarge, "RESTORE_DATABASE_TOO_LARGE", "浏览器可恢复数据库文件不超过 64 MiB 的备份。请使用命令行恢复此备份。")
		case errors.As(err, &fileError), errors.As(err, &renameError):
			writeRestoreStorageError(w, err)
		default:
			writeInvalidBackup(w)
		}
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Directory string `json:"directory"`
	}{destination})
}

func writeRestoreTimeout(w http.ResponseWriter) {
	writeError(w, http.StatusRequestTimeout, "RESTORE_TIMEOUT", "上传或恢复校验超时。可以使用命令行恢复。")
}

func writeRestoreTooLarge(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "浏览器一次可恢复 1 GiB 以内的备份。较大的备份请使用命令行恢复。")
}

func writeInvalidBackup(w http.ResponseWriter) {
	writeError(w, http.StatusUnprocessableEntity, "INVALID_BACKUP", "备份校验失败。请确认 ZIP 完整，并使用支持该资料库版本的程序。")
}

func writeRestoreStorageError(w http.ResponseWriter, err error) {
	log.Printf("workspace restore storage: %v", err)
	writeError(w, http.StatusInternalServerError, "RESTORE_STORAGE_FAILED", "无法写入恢复资料库。请检查可用空间和目录写入权限。")
}

// The exporter writes a comment-free, single-disk ZIP. Bound its directory
// before archive/zip allocates member records for an uploaded file.
func validateRestoreDirectory(file io.ReaderAt, size int64) error {
	const endSize = 22
	var end [endSize]byte
	endOffset := size - endSize
	if endOffset < 0 {
		return zip.ErrFormat
	}
	if _, err := file.ReadAt(end[:], endOffset); err != nil {
		return err
	}
	le := binary.LittleEndian
	if le.Uint32(end[:4]) != 0x06054b50 || le.Uint32(end[4:8]) != 0 || le.Uint16(end[20:]) != 0 ||
		le.Uint16(end[8:10]) != le.Uint16(end[10:12]) {
		return zip.ErrFormat
	}
	records := uint64(le.Uint16(end[10:12]))
	directorySize := uint64(le.Uint32(end[12:16]))
	directoryOffset := uint64(le.Uint32(end[16:20]))
	if records == 0xffff || directorySize == 0xffffffff || directoryOffset == 0xffffffff {
		var extended [56 + 20]byte
		endOffset -= int64(len(extended))
		if endOffset < 0 {
			return zip.ErrFormat
		}
		if _, err := file.ReadAt(extended[:], endOffset); err != nil {
			return err
		}
		if le.Uint32(extended[:4]) != 0x06064b50 || le.Uint64(extended[4:12]) != 44 ||
			le.Uint64(extended[16:24]) != 0 || le.Uint64(extended[24:32]) != le.Uint64(extended[32:40]) ||
			le.Uint32(extended[56:60]) != 0x07064b50 || le.Uint32(extended[60:64]) != 0 ||
			le.Uint64(extended[64:72]) != uint64(endOffset) || le.Uint32(extended[72:76]) != 1 {
			return zip.ErrFormat
		}
		records = le.Uint64(extended[32:40])
		directorySize = le.Uint64(extended[40:48])
		directoryOffset = le.Uint64(extended[48:56])
	}
	const headerSize = 46
	if directorySize > maxBackupDirectory || records < 2 || records > maxBackupObjects+2 || records > directorySize/headerSize ||
		directorySize > uint64(endOffset) || directoryOffset != uint64(endOffset)-directorySize {
		return zip.ErrFormat
	}
	// The standard ZIP reader discovers the actual record count while allocating
	// entries. Walk only the headers first to check that they fit the declared directory.
	directory := bufio.NewReader(io.NewSectionReader(file, int64(directoryOffset), int64(directorySize)))
	var header [headerSize]byte
	for range records {
		if _, err := io.ReadFull(directory, header[:]); err != nil {
			return err
		}
		if le.Uint32(header[:4]) != 0x02014b50 {
			return zip.ErrFormat
		}
		extra := int(le.Uint16(header[28:30])) + int(le.Uint16(header[30:32])) + int(le.Uint16(header[32:34]))
		if _, err := directory.Discard(extra); err != nil {
			return err
		}
	}
	if _, err := directory.ReadByte(); err != io.EOF {
		if err != nil {
			return err
		}
		return zip.ErrFormat
	}
	return nil
}
