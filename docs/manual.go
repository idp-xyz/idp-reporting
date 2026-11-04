// Package docs carries the user manual into the binary (S07-D): the console serves it at /console/manual/ so the help icon of every
// shell reaches it without a file share. Nothing else of docs/ is embedded.
package docs

import "embed"

// Manual is docs/manual as written: index.html, manual.css, img/ and testdata/.
//
//go:embed manual
var Manual embed.FS
