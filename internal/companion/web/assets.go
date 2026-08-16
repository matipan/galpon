// Package web contains the static Galpón phone companion frontend.
package web

import "embed"

// Assets contains the no-build browser application. The companion HTTP
// adapter can serve these files without depending on the source checkout.
//
//go:embed index.html styles.css app.mjs api.mjs mock-api.mjs
var Assets embed.FS
