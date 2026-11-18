// Package wordexport converts manuscript text to editable Word documents.
package wordexport

import (
	"context"
	_ "embed"
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(pythonExecutable) == "" {
		return nil, errors.New("Word export requires a configured Python executable")
	}

	// Isolated mode keeps the service's working directory and user site out
	// of Python's import path while retaining the selected interpreter's venv.
	cmd := exec.CommandContext(ctx, pythonExecutable, "-I", "-B", "-c", workerScript)
	cmd.Env = pythonEnvironment(os.Environ())
	cmd.Stdin = strings.NewReader(source)
	cmd.Stderr = io.Discard
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
			return nil, errors.New("Word export requires python-docx in the configured Python environment")
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
