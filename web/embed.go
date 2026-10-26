package web

import "embed"

//go:embed index.html app.js style.css view-model.mjs desktop-settings.mjs api-client.mjs app-lifecycle.mjs browser-actions.mjs ui.mjs task-controller.mjs task-view.mjs node-controller.mjs node-settings.mjs
var Files embed.FS
