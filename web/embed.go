// Package webui embeds the compiled React/Vite frontend.
// Run `npm run build` inside the web/ directory before building the Go binary.
package webui

import "embed"

// FS contains the compiled web UI from web/dist/.
// Build with: cd web && npm run build
//
//go:embed dist
var FS embed.FS
