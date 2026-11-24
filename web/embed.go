// Package web embeds the built React panel (web/dist) into the Go binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Panel returns the built panel. Before `npm run build` it only holds .gitkeep and the API serves a note instead.
func Panel() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return sub
}
