package theme

import (
	"embed"
	"io/fs"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

// Highlighting styles for the built-in themes chroma has none for. They
// join chroma's own, so [themes] can name them too.
//
//go:embed styles/*.xml
var styleFiles embed.FS

func init() {
	names, _ := fs.Glob(styleFiles, "styles/*.xml")
	for _, name := range names {
		f, _ := styleFiles.Open(name)
		style, err := chroma.NewXMLStyle(f)
		f.Close()
		if err != nil {
			panic(name + ": " + err.Error())
		}
		styles.Register(style)
	}
}
