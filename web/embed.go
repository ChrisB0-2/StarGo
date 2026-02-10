package web

import "embed"

// Content holds the embedded web frontend files (index.html, app.js, styles.css).
//
//go:embed index.html app.js styles.css
var Content embed.FS
