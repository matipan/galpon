// Package web contains the static Galpón phone companion frontend.
package web

import "embed"

// Assets contains the no-build browser application. The companion HTTP
// adapter can serve these files without depending on the source checkout.
//
//go:embed index.html styles.css app.mjs api.mjs audio-policy.mjs mock-api.mjs activity-order.mjs detail-state.mjs timeline-state.mjs mobile-viewport.mjs companion-state.mjs rich-text.mjs performance.mjs presentation.mjs work-state.mjs operations-state.mjs manifest.webmanifest icon.svg
var Assets embed.FS
