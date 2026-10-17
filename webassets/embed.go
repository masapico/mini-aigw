package webassets

import (
	"embed"
	"io/fs"
)

//go:embed all:static all:templates
var Files embed.FS

func StaticFS() fs.FS {
	sub, err := fs.Sub(Files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

func TemplateFS() fs.FS {
	sub, err := fs.Sub(Files, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}
