package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// GetDistFS returns the sub-filesystem for the embedded dist folder
func GetDistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
