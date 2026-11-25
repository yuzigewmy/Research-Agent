package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"research-agent/internal/inference"
)

const (
	imageReviewRule        = 1
	imageReviewTimeout     = 60 * time.Second
	maxImageReviewPixels   = 4_000_000
	maxImageReviewEdge     = 4096
	maxImageReviewResponse = 12 << 10
)

var (
	errImageReviewConnection   = errors.New("视觉模型服务地址已变更。请恢复原地址，或重新提交图片复查。")
	errImageReviewOutput       = errors.New("模型回复未形成有效的图片复查记录。请重试。")
	errImageReviewUnverifiable = errors.New("模型未能从图片中取得足够的观察依据。请核对原图清晰度后重试。")
)

//go:embed prompts/image_review_v1.md
var imageReviewPrompt string

func (s *Store) runImageReview(ctx context.Context, model *inference.Client, projectID string, task Task) (taskResult, error) {
	if model == nil || task.model.Model == "" {
		return taskResult{}, errModelUnavailable
	}
	if model.ConnectionFingerprint() != task.model.connection {
		return taskResult{}, errImageReviewConnection
	}
	model = model.ForModel(task.model.Model, task.model.ReasoningEffort)
	var materialID, digest string
	var rule int
	if err := s.db.QueryRowContext(ctx, `SELECT material_id, source_sha256, rule_revision FROM image_reviews
		WHERE project_id = ? AND task_id = ?`, projectID, task.TaskID).Scan(&materialID, &digest, &rule); err != nil {
		return taskResult{}, err
	}
	if rule != imageReviewRule {
		return taskResult{}, errors.New("unknown image review rule revision")
	}
	material, err := s.getMaterial(ctx, projectID, materialID)
	if err != nil {
		return taskResult{}, err
	}
	format := imageReviewType(material.Name)
	if material.SHA256 != digest || format == "" || material.Size <= 0 || material.Size > maxImageReviewBytes {
		return taskResult{}, errImageReviewInput
	}
	file, err := s.openObject(digest)
	if err != nil {
		return taskResult{}, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxImageReviewBytes+1))
	if err != nil {
		return taskResult{}, err
	}
	hash := sha256.Sum256(body)
	if int64(len(body)) != material.Size || hex.EncodeToString(hash[:]) != digest {
		return taskResult{}, errImageReviewInput
	}
	if err := validateImageReviewFile(ctx, format, body); err != nil {
		return taskResult{}, err
	}
	var output strings.Builder
	if err := model.StreamImage(ctx, imageReviewPrompt, format, body, 2048, func(text string) error {
		if len(text) > maxImageReviewResponse-output.Len() {
			return errImageReviewOutput
		}
		output.WriteString(text)
		return nil
	}); err != nil {
		return taskResult{}, err
	}
	result, err := parseImageReview(output.String())
	if err != nil {
		return taskResult{}, err
	}
	encoded, err := json.Marshal(result)
	return taskResult{jsonArtifact: string(encoded)}, err
}

func validateImageReviewFile(ctx context.Context, format string, body []byte) error {
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
		return errImageReviewInput
	}
	config, err := readConfig(reader)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		config.Width > maxImageReviewEdge || config.Height > maxImageReviewEdge || config.Width*config.Height > maxImageReviewPixels {
		return errImageReviewInput
	}
	// The JPEG decoder buffers ahead, so its reader cannot locate the actual end marker.
	if format == "jpeg" {
		if err := checkImageReviewJPEG(ctx, body); err != nil {
			return err
		}
	}
	input.Reset(body)
	_, err = decode(reader)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || (format == "png" && input.Len() != 0) {
		return errImageReviewInput
	}
	return nil
}

// Check the JPEG envelope before decoding pixels. Progressive scans may revisit
// the whole image, so the number of scans also needs a bound.
func checkImageReviewJPEG(ctx context.Context, body []byte) error {
	const maxScans = 32
	if !bytes.HasPrefix(body, []byte{0xff, 0xd8}) {
		return errImageReviewInput
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
				return errImageReviewInput
			}
		}
	}
	return errImageReviewInput
}

func parseImageReview(text string) (imageReviewResult, error) {
	var result imageReviewResult
	if !utf8.ValidString(text) {
		return result, errImageReviewOutput
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return imageReviewResult{}, errImageReviewOutput
	}
	if result.Verdict == "UNVERIFIABLE" {
		return imageReviewResult{}, errImageReviewUnverifiable
	}
	for _, list := range [][]string{result.ObservedLabels, result.Relations, result.Issues} {
		if list == nil || len(list) > 16 {
			return imageReviewResult{}, errImageReviewOutput
		}
		for i, item := range list {
			list[i] = strings.TrimSpace(item)
			if list[i] == "" || len(list[i]) > 256 || strings.ContainsFunc(list[i], unicode.IsControl) {
				return imageReviewResult{}, errImageReviewOutput
			}
		}
	}
	switch result.Verdict {
	case "PASS":
		if len(result.Issues) == 0 && len(result.ObservedLabels)+len(result.Relations) > 0 {
			return result, nil
		}
	case "ISSUES":
		if len(result.Issues) > 0 {
			return result, nil
		}
	}
	return imageReviewResult{}, errImageReviewOutput
}
