package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distribution embed.FS

func Assets() (fs.FS, error) { return fs.Sub(distribution, "dist") }
