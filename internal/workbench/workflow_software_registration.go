package workbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"research-agent/internal/inference"
)

const softwareRegistrationWorkflow = "software-registration-v1"
const defaultSoftwareVersion = "V1.0"
const maxSoftwareSourceBytes = 64 << 10
const softwareProgramHeading = "程序材料\n\n"
const softwareProgramMissing = "本轮未选择源码。请补充用于登记的软件源码。\n"

//go:embed prompts/software_registration_manual.md
var softwareManualPrompt string

var (
	errSoftwareOptions         = errors.New("invalid software registration options")
	errSoftwareSources         = errors.New("源码文本合计不得超过 64 KiB。请缩小材料范围。")
	errInvalidSoftwareMaterial = errors.New("invalid software registration material")
)

type softwareOptions struct {
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	MaterialIDs []string `json:"materialIds,omitempty"`
}

type softwareSettings struct {
	softwareOptions
	Materials []TurnReference `json:"materials,omitempty"`
}

func softwareSourcePath(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".cs", ".go", ".java", ".kt", ".kts", ".rs", ".swift",
		".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".py", ".pyw", ".rb", ".php", ".lua", ".r", ".jl",
		".scala", ".sql", ".sh", ".ps1", ".html", ".css", ".vue", ".svelte":
		return true
	default:
		return false
	}
}

func freezeSoftwareSettings(ctx context.Context, tx *sql.Tx, projectID string, input *softwareOptions) (string, error) {
	var options softwareOptions
	if input != nil {
		options = *input
	}
	options.Version = strings.TrimSpace(options.Version)
	if options.Version == "" {
		options.Version = defaultSoftwareVersion
	}
	options.Description = strings.TrimSpace(options.Description)
	if len(options.Version) > 64 || strings.ContainsFunc(options.Version, unicode.IsControl) ||
		len(options.Description) > 4<<10 || strings.ContainsRune(options.Description, 0) || !validTurnReferences(options.MaterialIDs, nil) {
		return "", errSoftwareOptions
	}
	materials := make([]TurnReference, 0, len(options.MaterialIDs))
	var total int64
	for _, id := range options.MaterialIDs {
		material, err := scanMaterial(tx.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM materials
			WHERE project_id = ? AND material_id = ?`, projectID, id))
		if errors.Is(err, sql.ErrNoRows) {
			return "", errInvalidSoftwareMaterial
		}
		if err != nil {
			return "", err
		}
		if material.Kind != "text" && material.Format != "word" && material.Format != softwareArchiveFormat {
			return "", errInvalidSoftwareMaterial
		}
		if softwareSourcePath(material.Name) {
			total += material.Size
		}
		materials = append(materials, TurnReference{MaterialID: id, Name: material.Name, SHA256: material.SHA256, Format: material.Format})
	}
	if total > maxSoftwareSourceBytes {
		return "", errSoftwareSources
	}
	body, err := json.Marshal(softwareSettings{softwareOptions: options, Materials: materials})
	return string(body), err
}

type softwareSourceText struct {
	name, digest, body string
}

type softwareRunInputs struct {
	archives map[string][]softwareArchiveMember
	sources  []softwareSourceText
}

func (s *Store) readSoftwareInputs(ctx context.Context, materials []TurnReference) (softwareRunInputs, error) {
	inputs := softwareRunInputs{archives: make(map[string][]softwareArchiveMember)}
	for _, material := range materials {
		switch {
		case material.Format == softwareArchiveFormat:
			members, err := s.readSoftwareArchive(ctx, material)
			if err != nil {
				return softwareRunInputs{}, err
			}
			inputs.archives[material.MaterialID] = members
			for _, member := range members {
				if member.isSource {
					inputs.sources = append(inputs.sources, softwareSourceText{material.Name + "/" + member.name, member.digest, member.source})
				}
			}
		case softwareSourcePath(material.Name):
			content, err := os.ReadFile(s.materialObject(material.SHA256))
			if err != nil {
				return softwareRunInputs{}, err
			}
			text := string(content)
			if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || fmt.Sprintf("%x", sha256.Sum256(content)) != material.SHA256 {
				return softwareRunInputs{}, workflowOutputError{errors.New("固定源码的编码或指纹无法核对。")}
			}
			inputs.sources = append(inputs.sources, softwareSourceText{material.Name, material.SHA256, text})
		}
	}
	var total int
	for _, source := range inputs.sources {
		total += len(source.body)
	}
	if total > maxSoftwareSourceBytes {
		return softwareRunInputs{}, workflowOutputError{errSoftwareSources}
	}
	return inputs, nil
}

func softwareMaterialList(title string, settings softwareSettings, inputs softwareRunInputs) string {
	var list strings.Builder
	fmt.Fprintf(&list, "# 软件登记材料清单\n\n- 软件全称 %s\n- 版本号 %s\n", title, settings.Version)
	if len(settings.Materials) == 0 {
		list.WriteString("\n本轮无上传材料。请核对软件功能并补充源码和文档。\n")
	}
	for _, material := range settings.Materials {
		category := "说明文档"
		switch {
		case material.Format == softwareArchiveFormat:
			category = "压缩包"
		case softwareSourcePath(material.Name):
			category = "源码文本"
		}
		fmt.Fprintf(&list, "\n## %s\n\n- 文件 %s\n- 材料编号 `%s`\n- SHA-256 `%s`\n",
			category, material.Name, material.MaterialID, material.SHA256)
		for _, member := range inputs.archives[material.MaterialID] {
			fmt.Fprintf(&list, "- 成员 %s/%s · SHA-256 `%s`\n", material.Name, member.name, member.digest)
		}
	}
	return list.String()
}

func softwareProgramMaterials(sources []softwareSourceText) string {
	var program strings.Builder
	program.WriteString(softwareProgramHeading)
	for _, source := range sources {
		fmt.Fprintf(&program, "文件 %s\nSHA-256 %s\n字节数 %d\n\n", source.name, source.digest, len(source.body))
		program.WriteString(source.body)
		program.WriteString("\n\n")
	}
	if len(sources) == 0 {
		program.WriteString(softwareProgramMissing)
	}
	return program.String()
}

func (s *Store) softwareDocumentExcerpts(ctx context.Context, materials []TurnReference, inputs softwareRunInputs) ([]turnMaterialExcerpt, error) {
	excerpts := make([]turnMaterialExcerpt, 0, maxTurnReferences)
	for _, material := range materials {
		if len(excerpts) == maxTurnReferences {
			break
		}
		if material.Format == softwareArchiveFormat {
			for _, member := range inputs.archives[material.MaterialID] {
				if member.preview != nil {
					ref := TurnReference{MaterialID: material.MaterialID, Name: material.Name + "/" + member.name, SHA256: member.digest}
					excerpts = append(excerpts, turnMaterialExcerpt{ref, *member.preview})
					if len(excerpts) == maxTurnReferences {
						break
					}
				}
			}
		} else if !softwareSourcePath(material.Name) {
			preview, err := s.materialExcerpt(ctx, material.SHA256, material.Format, referenceExcerptBytes)
			if err != nil {
				return nil, err
			}
			excerpts = append(excerpts, turnMaterialExcerpt{material, preview})
		}
	}
	return excerpts, nil
}

func (s *Store) generateSoftwareRegistrationStep(ctx context.Context, model *inference.Client, project Project, task Task, input, options string) (taskResult, error) {
	var settings softwareSettings
	if err := json.Unmarshal([]byte(options), &settings); err != nil {
		return taskResult{}, err
	}
	stage := workflowStages[softwareRegistrationWorkflow][task.workflowStep]
	if stage.key == "application" {
		_, manual, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "manuscript")
		if err != nil {
			return taskResult{}, err
		}
		_, program, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "program")
		if err != nil {
			return taskResult{}, err
		}
		_, materials, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "sources")
		if err != nil {
			return taskResult{}, err
		}
		text := fmt.Sprintf("软件著作权登记申请信息核对单\n\n软件全称 %s\n版本号 %s\n著作权人 待填写\n开发完成日期 待核实\n首次发表情况 待核实\n运行环境 待核实\n\n操作手册 SHA-256 %x\n程序材料 SHA-256 %x\n",
			project.Title, settings.Version, sha256.Sum256([]byte(manual)), sha256.Sum256([]byte(program)))
		return taskResult{markdown: text + "\n" + materials}, nil
	}

	inputs, err := s.readSoftwareInputs(ctx, settings.Materials)
	if err != nil {
		return taskResult{}, err
	}
	switch stage.key {
	case "sources":
		return taskResult{markdown: softwareMaterialList(project.Title, settings, inputs)}, nil
	case "program":
		return taskResult{markdown: softwareProgramMaterials(inputs.sources)}, nil
	default:
		excerpts, err := s.softwareDocumentExcerpts(ctx, settings.Materials, inputs)
		if err != nil {
			return taskResult{}, err
		}
		evidence, err := json.Marshal(excerpts)
		if err != nil {
			return taskResult{}, err
		}
		body, err := streamTaskMarkdown(ctx, model, softwareManualPrompt,
			input+"\n\n登记设置（JSON）\n"+options+"\n\n固定文档节选（JSON）\n"+string(evidence))
		return taskResult{markdown: body}, err
	}
}
