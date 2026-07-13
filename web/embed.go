// Package web holds the dashboard's static assets, compiled into the binary.
//
// The page is served as three plain files — no build step, no framework, no CDN:
// index.html at GET /dashboard, the rest under GET /web/*. It polls /api/stats
// every 2s and draws what Go already folded.
package web

import "embed"

//go:embed index.html app.css app.js logo.svg
var FS embed.FS
