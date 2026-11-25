package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	maxMaterialImageBytes  = 8 << 20
	maxMaterialImagePixels = 4_000_000
	maxMaterialImageEdge   = 4096
)

var errMaterialImage = errors.New("请使用有效的静态 PNG 或 JPEG 原件。文件上限为 8 MiB，长宽各不超过 4096 像素，总像素数不超过 400 万。")

func materialImageType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "png"
	case ".jpg", ".jpeg":
		return "jpeg"
	}
	return ""
}

func (s *Store) handleMaterialImagePreview(w http.ResponseWriter, r *http.Request) {
	ctx, projectID, materialID := r.Context(), r.PathValue("id"), r.PathValue("materialId")
	material, err := s.getMaterial(ctx, projectID, materialID)
	if err != nil {
		writeMaterialError(w, "read material for image preview", err)
		return
	}
	body, format, err := s.readMaterialImage(ctx, material)
	if errors.Is(err, errMaterialImage) {
		writeError(w, http.StatusUnprocessableEntity, "IMAGE_PREVIEW_UNAVAILABLE", err.Error())
		return
	}
	if err != nil {
		writeStoreError(w, "read image preview", err)
		return
	}
	w.Header().Set("Content-Type", "image/"+format)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": material.Name}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Source-SHA256", material.SHA256)
	http.ServeContent(w, r, material.Name, time.Time{}, bytes.NewReader(body))
}

func (s *Store) readMaterialImage(ctx context.Context, material Material) ([]byte, string, error) {
	format := materialImageType(material.Name)
	if format == "" || material.Size <= 0 || material.Size > maxMaterialImageBytes {
		return nil, "", errMaterialImage
	}
	file, err := s.openObject(material.SHA256)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, material.Size+1))
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(body)
	if int64(len(body)) != material.Size || hex.EncodeToString(hash[:]) != material.SHA256 {
		return nil, "", errMaterialImage
	}
	if err := validateMaterialImage(ctx, format, body); err != nil {
		return nil, "", err
	}
	return body, format, nil
}

func validateMaterialImage(ctx context.Context, format string, body []byte) error {
	input := bytes.NewReader(body)
	reader := contextReader{ctx, input}
	var readConfig func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	switch format {
	case "png":
		readConfig, decode = png.DecodeConfig, png.Decode
	case "jpeg":
		readConfig, decode = jpeg.DecodeConfig, jpeg.Decode
	default:
		return errMaterialImage
	}
	config, err := readConfig(reader)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		config.Width > maxMaterialImageEdge || config.Height > maxMaterialImageEdge || config.Width*config.Height > maxMaterialImagePixels {
		return errMaterialImage
	}
	if format == "png" {
		err = checkStaticMaterialPNG(ctx, body[8:]) // DecodeConfig has verified the PNG signature.
	} else {
		// JPEG buffers ahead, so the decoder's reader cannot locate its end marker.
		err = checkMaterialJPEG(ctx, body)
	}
	if err != nil {
		return err
	}
	input.Reset(body)
	_, err = decode(reader)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return errMaterialImage
	}
	return nil
}

// The Go PNG decoder reads a single image. Reject APNG chunks so the browser
// cannot render additional frames outside the validated pixel budget.
func checkStaticMaterialPNG(ctx context.Context, chunks []byte) error {
	const chunkOverhead = 12
	for len(chunks) >= chunkOverhead {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(chunks[:4])
		if uint64(size) > uint64(len(chunks)-chunkOverhead) {
			break
		}
		switch string(chunks[4:8]) {
		case "acTL", "fcTL", "fdAT":
			return errMaterialImage
		case "IEND":
			if size == 0 && len(chunks) == chunkOverhead {
				return nil
			}
			return errMaterialImage
		}
		chunks = chunks[int(size)+chunkOverhead:]
	}
	return errMaterialImage
}

// Check the JPEG envelope before decoding pixels. Progressive scans may revisit
// the whole image, so the number of scans also needs a bound.
func checkMaterialJPEG(ctx context.Context, body []byte) error {
	const maxScans = 32
	if !bytes.HasPrefix(body, []byte{0xff, 0xd8}) {
		return errMaterialImage
	}
	body = body[2:]
	inScan, scans := false, 0
	for len(body) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if inScan {
			offset := bytes.IndexByte(body, 0xff)
			if offset == -1 {
				break
			}
			body = body[offset:]
		} else if body[0] != 0xff {
			break
		}
		i := 1
		for i < len(body) && body[i] == 0xff {
			i++
		}
		if i == len(body) {
			break
		}
		marker := body[i]
		body = body[i+1:]
		if inScan && (marker == 0 || (0xd0 <= marker && marker <= 0xd7)) {
			continue
		}
		if marker == 0xd9 {
			if len(body) == 0 && scans > 0 {
				return nil
			}
			break
		}
		if marker == 0 || marker == 0xd8 || (0xd0 <= marker && marker <= 0xd7) || len(body) < 2 {
			break
		}
		size := int(binary.BigEndian.Uint16(body[:2]))
		if size < 2 || size > len(body) {
			break
		}
		body = body[size:]
		inScan = marker == 0xda
		if inScan {
			scans++
			if scans > maxScans {
				return errMaterialImage
			}
		}
	}
	return errMaterialImage
}
