//go:build !embedui

// Package webui provides an empty FS when the UI is not embedded.
// The gateway's /ui/* routes are skipped when WebUI is nil.
package webui

import "embed"

// FS is empty; the React UI is served by a separate nginx service.
var FS embed.FS
