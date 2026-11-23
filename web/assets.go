package webassets

import (
	"io/fs"
	"os"
)

var assetsRoot fs.FS = os.DirFS("web")

// Files selects the Web build directory or the assets embedded at compile time.
func Files(directory string) (fs.FS, error) {
	if directory != "" {
		return os.DirFS(directory), nil
	}
	return fs.Sub(assetsRoot, "dist")
}
