package web

import (
	"embed"
	"io/fs"
)

// Assets are built by Vite and embedded in the production binary.
//
//go:embed all:dist
var Assets embed.FS

func Dist() fs.FS {
	sub, err := fs.Sub(Assets, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
