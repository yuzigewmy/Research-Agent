//go:build embedweb

package webassets

import "embed"

//go:embed dist/index.html dist/assets dist/pdf-resources
var bundled embed.FS

func init() {
	assetsRoot = bundled
}
