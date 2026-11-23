// Package wordexport converts manuscript text to editable Word documents.
package wordexport

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

//go:embed export_docx.py
var workerScript string

// Export converts a UTF-8 Markdown manuscript to DOCX bytes. It supports ATX
// headings, paragraphs, simple emphasis, inline and fenced code, flat lists,
// and pipe tables. The worker reads the manuscript only from standard input.
// The operator-configured interpreter must have python-docx installed in its
// system or virtual environment site-packages.
func Export(ctx context.Context, pythonExecutable string, source string) ([]byte, error) {
	return exportDocument(ctx, pythonExecutable, strings.NewReader(source))
}

// Figure describes an already validated flow diagram and its editable caption.
type Figure struct {
	Caption string       `json:"caption"`
	Nodes   []FigureNode `json:"nodes"`
	Edges   []FigureEdge `json:"edges"`
}

// FigureNode positions a node's top-left corner on the diagram canvas.
type FigureNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
}

// FigureEdge connects two node IDs in the same diagram.
type FigureEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ExportWithFigures appends validated flow diagrams to an editable manuscript.
// Figure export requires Pillow and an operator-configured local font with
// coverage for the labels. An empty figure list uses the plain Export path.
func ExportWithFigures(ctx context.Context, pythonExecutable, fontPath, source string, figures []Figure) ([]byte, error) {
	if len(figures) == 0 {
		return Export(ctx, pythonExecutable, source)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(fontPath) == "" {
		return nil, errors.New("Word figure export requires a configured font file")
	}
	payload, err := json.Marshal(struct {
		FontPath string   `json:"fontPath"`
		Source   string   `json:"source"`
		Figures  []Figure `json:"figures"`
	}{FontPath: fontPath, Source: source, Figures: figures})
	if err != nil {
		return nil, fmt.Errorf("encode Word figure export: %w", err)
	}
	return exportDocument(ctx, pythonExecutable, bytes.NewReader(payload), "--figures")
}

func exportDocument(ctx context.Context, pythonExecutable string, input io.Reader, workerArgs ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(pythonExecutable) == "" {
		return nil, errors.New("Word export requires a configured Python executable")
	}

	// Isolated mode keeps the service's working directory and user site out
	// of Python's import path while retaining the selected interpreter's venv.
	args := append([]string{"-I", "-B", "-c", workerScript}, workerArgs...)
	cmd := exec.CommandContext(ctx, pythonExecutable, args...)
	cmd.Env = pythonEnvironment(os.Environ())
	cmd.Stdin = input
	cmd.Stderr = io.Discard
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 3:
				return nil, errors.New("Word export requires python-docx in the configured Python environment")
			case 4:
				return nil, errors.New("Word figure export requires Pillow in the configured Python environment")
			case 5:
				return nil, errors.New("Word figure export could not load the configured font file")
			}
		}
		return nil, fmt.Errorf("export Word document: %w", err)
	}
	if len(output) == 0 {
		return nil, errors.New("Word export produced an empty document")
	}
	return output, nil
}

func pythonEnvironment(parent []string) []string {
	// An allowlist avoids passing model provider credentials under any name.
	// Keep paths, locale and temporary-directory settings used during startup.
	env := make([]string, 0, len(parent))
	for _, entry := range parent {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "PATH", "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC",
			"TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
			"APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE":
			env = append(env, entry)
		}
	}
	return env
}
