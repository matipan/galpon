// Package web contains the static Galpón phone companion frontend.
package web

import "embed"

// Assets contains the no-build browser application. The companion HTTP
// adapter can serve these files without depending on the source checkout.
//
//go:embed index.html styles.css app.mjs api.mjs audio-policy.mjs mock-api.mjs activity-order.mjs detail-state.mjs timeline-state.mjs mobile-viewport.mjs companion-state.mjs rich-text.mjs performance.mjs presentation.mjs theme.mjs icons.mjs manifest.webmanifest icon.svg apple-touch-icon.png fonts/geist-latin.woff2 fonts/geist-mono-latin.woff2 fonts/Geist-OFL.txt fonts/GeistMono-OFL.txt
var Assets embed.FS
