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

//go:embed prompts/software_registration_manual.md
var softwareManualPrompt string

var (
	errSoftwareOptions = errors.New("invalid software registration options")
	errSoftwareSources = errors.New("software source text exceeds 64 KiB")
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
	materials, err := resolveTurnReferences(ctx, tx, projectID, options.MaterialIDs, nil)
	if err != nil {
		return "", err
	}
	var total int64
	for _, material := range materials {
		if !softwareSourcePath(material.Name) {
			continue
		}
		var size int64
		if err := tx.QueryRowContext(ctx, `SELECT size FROM materials WHERE project_id = ? AND material_id = ?`,
			projectID, material.MaterialID).Scan(&size); err != nil {
			return "", err
		}
		total += size
	}
	if total > maxSoftwareSourceBytes {
		return "", errSoftwareSources
	}
	body, err := json.Marshal(softwareSettings{softwareOptions: options, Materials: materials})
	return string(body), err
}

func softwareMaterialList(title string, settings softwareSettings) string {
	var list strings.Builder
	fmt.Fprintf(&list, "# 软件登记材料清单\n\n- 软件全称 %s\n- 版本号 %s\n", title, settings.Version)
	if len(settings.Materials) == 0 {
		list.WriteString("\n本轮无上传材料。请核对软件功能并补充源码和文档。\n")
	}
	for _, material := range settings.Materials {
		category := "说明文档"
		if softwareSourcePath(material.Name) {
			category = "源码文本"
		}
		fmt.Fprintf(&list, "\n## %s\n\n- 文件 %s\n- 材料编号 `%s`\n- SHA-256 `%s`\n",
			category, material.Name, material.MaterialID, material.SHA256)
	}
	return list.String()
}

func (s *Store) softwareProgramMaterials(settings softwareSettings) (string, error) {
	var program strings.Builder
	program.WriteString("程序材料\n\n")
	for _, material := range settings.Materials {
		if !softwareSourcePath(material.Name) {
			continue
		}
		content, err := os.ReadFile(s.materialObject(material.SHA256))
		if err != nil {
			return "", err
		}
		if !utf8.Valid(content) || strings.ContainsRune(string(content), 0) || fmt.Sprintf("%x", sha256.Sum256(content)) != material.SHA256 {
			return "", workflowOutputError{errors.New("固定源码的编码或指纹无法核对。")}
		}
		fmt.Fprintf(&program, "文件 %s\nSHA-256 %s\n字节数 %d\n\n", material.Name, material.SHA256, len(content))
		program.Write(content)
		program.WriteString("\n\n")
	}
	if program.Len() == len("程序材料\n\n") {
		program.WriteString("本轮未选择源码。请补充用于登记的软件源码。\n")
	}
	return program.String(), nil
}

func (s *Store) generateSoftwareRegistrationStep(ctx context.Context, model *inference.Client, project Project, task Task, input, options string) (taskResult, error) {
	var settings softwareSettings
	if err := json.Unmarshal([]byte(options), &settings); err != nil {
		return taskResult{}, err
	}
	stage := workflowStages[softwareRegistrationWorkflow][task.workflowStep]
	switch stage.key {
	case "sources":
		return taskResult{markdown: softwareMaterialList(project.Title, settings)}, nil
	case "program":
		body, err := s.softwareProgramMaterials(settings)
		return taskResult{markdown: body}, err
	case "application":
		_, manual, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "manuscript")
		if err != nil {
			return taskResult{}, err
		}
		_, program, err := s.workflowArtifact(ctx, project.ProjectID, task.workflowRunID, "program")
		if err != nil {
			return taskResult{}, err
		}
		text := fmt.Sprintf("软件著作权登记申请信息核对单\n\n软件全称 %s\n版本号 %s\n著作权人 待填写\n开发完成日期 待核实\n首次发表情况 待核实\n运行环境 待核实\n\n操作手册 SHA-256 %x\n程序材料 SHA-256 %x\n",
			project.Title, settings.Version, sha256.Sum256([]byte(manual)), sha256.Sum256([]byte(program)))
		return taskResult{markdown: text + "\n" + softwareMaterialList(project.Title, settings)}, nil
	default:
		var references []TurnReference
		for _, material := range settings.Materials {
			if !softwareSourcePath(material.Name) {
				references = append(references, material)
			}
		}
		excerpts, err := s.turnMaterialExcerpts(ctx, references)
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
