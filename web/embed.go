package web

import "embed"

//go:embed index.html app.js style.css i18n.mjs i18n-catalog.mjs locale-controller.mjs view-model.mjs desktop-settings.mjs api-client.mjs app-lifecycle.mjs browser-actions.mjs ui.mjs task-controller.mjs task-admission.mjs task-view.mjs node-controller.mjs node-settings.mjs local-commands.mjs command-run.mjs graph-model.mjs graph-editor.mjs graph-canvas.mjs graph-inspector.mjs graph-menu.mjs graph-editor.css
var Files embed.FS
