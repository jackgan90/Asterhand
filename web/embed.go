package web

import "embed"

//go:embed index.html app.js style.css view-model.mjs desktop-settings.mjs
var Files embed.FS
