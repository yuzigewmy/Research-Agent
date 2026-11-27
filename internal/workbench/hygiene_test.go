package workbench

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRepositoryHygieneAndTemplateContracts(t *testing.T) {
	t.Run("RepositoryTextHygiene", func(t *testing.T) {
		repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatalf("resolve repo root: %v", err)
		}
		if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
			t.Fatalf("expected go.mod at repo root %s: %v", repoRoot, err)
		}

		// Dynamically assemble patterns so this test file never contains forbidden literals.
		apiKeyPattern := regexp.MustCompile(`\b` + "sk" + `-[A-Za-z0-9_-]{20,}\b`)
		windowsUserPathPattern := regexp.MustCompile(`(?i)` + "C:" + `[/\\]` + "Users" + `[/\\]`)
		forbiddenBrandWord := regexp.MustCompile(`\b` + "R" + "EA" + `\b`)
		forbiddenDirToken := regexp.MustCompile(`(?i)(?:^|[/\\\s"'` + "`" + `])(?:\.` + "r" + "ea" + `|` + "r" + "ea" + `[-_]workspace)(?:[/\\\s"'` + "`" + `]|$)`)

		allowedExts := map[string]bool{
			".go":   true,
			".ts":   true,
			".tsx":  true,
			".css":  true,
			".md":   true,
			".json": true,
			".html": true,
			".txt":  true,
			".yml":  true,
			".yaml": true,
		}
		scanDirs := []string{"cmd", "internal", filepath.Join("web", "src"), "docs"}

		scannedFiles := 0
		for _, relDir := range scanDirs {
			absDir := filepath.Join(repoRoot, relDir)
			err := filepath.WalkDir(absDir, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					name := entry.Name()
					if name == "testdata" || name == "node_modules" || name == "dist" || name == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				if !allowedExts[strings.ToLower(filepath.Ext(entry.Name()))] {
					return nil
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				scannedFiles++
				relPath, _ := filepath.Rel(repoRoot, path)
				text := string(raw)

				if match := apiKeyPattern.FindString(text); match != "" {
					t.Errorf("%s contains API-key-like secret token (len=%d)", relPath, len(match))
				}
				if match := windowsUserPathPattern.FindString(text); match != "" {
					t.Errorf("%s contains hardcoded local user directory path %q", relPath, match)
				}
				if match := forbiddenBrandWord.FindString(text); match != "" {
					t.Errorf("%s contains forbidden reference brand identifier %q", relPath, match)
				}
				if match := forbiddenDirToken.FindString(text); match != "" {
					t.Errorf("%s contains forbidden reference directory token %q", relPath, strings.TrimSpace(match))
				}
				return nil
			})
			if err != nil {
				t.Fatalf("scan %s: %v", relDir, err)
			}
		}
		if scannedFiles < 50 {
			t.Fatalf("expected to scan at least 50 repository text files, got %d", scannedFiles)
		}
	})

	t.Run("ProjectTemplatesAndPromptContracts", func(t *testing.T) {
		const wantTemplateCount = 21
		if len(projectTemplates) != wantTemplateCount {
			t.Fatalf("expected %d project templates, got %d", wantTemplateCount, len(projectTemplates))
		}

		seenIDs := make(map[string]bool, len(projectTemplates))
		workflowTemplateCount := 0
		for _, tpl := range projectTemplates {
			if strings.TrimSpace(tpl.ID) == "" {
				t.Fatalf("found template with empty ID: %+v", tpl)
			}
			if seenIDs[tpl.ID] {
				t.Fatalf("duplicate project template ID: %s", tpl.ID)
			}
			seenIDs[tpl.ID] = true

			if strings.TrimSpace(tpl.Label) == "" {
				t.Errorf("template %s has empty Label", tpl.ID)
			}
			if strings.TrimSpace(tpl.Summary) == "" {
				t.Errorf("template %s has empty Summary", tpl.ID)
			}
			if strings.TrimSpace(tpl.Revision) == "" {
				t.Errorf("template %s has empty Revision", tpl.ID)
			}
			if len(tpl.Deliverables) == 0 {
				t.Errorf("template %s has no Deliverables", tpl.ID)
			}

			if tpl.Kind == "workflow" {
				workflowTemplateCount++
				stages, ok := workflowStages[tpl.Revision]
				if !ok || len(stages) == 0 {
					t.Fatalf("workflow template %s (revision %q) has no mapped stages", tpl.ID, tpl.Revision)
				}
				seenStepKeys := make(map[string]bool, len(stages))
				for idx, stage := range stages {
					if strings.TrimSpace(stage.key) == "" || strings.TrimSpace(stage.title) == "" {
						t.Errorf("workflow %s step %d has empty key/title: %+v", tpl.ID, idx, stage)
					}
					if seenStepKeys[stage.key] {
						t.Errorf("workflow %s has duplicate step key %q", tpl.ID, stage.key)
					}
					seenStepKeys[stage.key] = true
					if stage.prompt != "" && strings.TrimSpace(stage.prompt) == "" {
						t.Errorf("workflow %s step %s has whitespace-only prompt", tpl.ID, stage.key)
					}
				}
			}
		}
		if workflowTemplateCount != 17 {
			t.Fatalf("expected 17 workflow templates, got %d", workflowTemplateCount)
		}

		// Verify all prompt files under internal/workbench/prompts/*.md are readable, non-empty, valid UTF-8.
		promptEntries, err := os.ReadDir("prompts")
		if err != nil {
			t.Fatalf("read prompts directory: %v", err)
		}
		promptCount := 0
		for _, entry := range promptEntries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			promptCount++
			raw, err := os.ReadFile(filepath.Join("prompts", entry.Name()))
			if err != nil {
				t.Errorf("read prompt %s: %v", entry.Name(), err)
				continue
			}
			if !utf8.Valid(raw) {
				t.Errorf("prompt %s is not valid UTF-8", entry.Name())
			}
			if strings.TrimSpace(string(raw)) == "" {
				t.Errorf("prompt %s is empty", entry.Name())
			}
		}
		if promptCount == 0 {
			t.Fatal("no prompt templates found under internal/workbench/prompts")
		}

		// Verify all 22 math modeling contests have unique IDs and non-empty metadata.
		contests := allMathModelingContests()
		if len(contests) != 22 {
			t.Fatalf("expected 22 math modeling contests, got %d", len(contests))
		}
		seenContests := make(map[string]bool, len(contests))
		for _, c := range contests {
			if strings.TrimSpace(c.ID) == "" || seenContests[c.ID] {
				t.Fatalf("invalid or duplicate contest ID %q", c.ID)
			}
			seenContests[c.ID] = true
			if strings.TrimSpace(c.Name) == "" || len(c.Reminders) == 0 {
				t.Errorf("contest %s has incomplete metadata: %+v", c.ID, c)
			}
		}
	})
}
