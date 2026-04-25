//go:build embedui

// Package webui embeds the compiled React/Vite frontend.
// Build with: cd web && npm run build && go build -tags embedui ...
package webui

import "embed"

// FS contains the compiled web UI from web/dist/.
// Build with: cd web && npm run build
//
//go:embed dist
var FS embed.FS
