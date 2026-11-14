package web

import "embed"

//go:embed index.html app.js style.css i18n.mjs i18n-catalog.mjs locale-controller.mjs view-model.mjs desktop-settings.mjs desktop-shortcut.mjs api-client.mjs app-lifecycle.mjs browser-actions.mjs ui.mjs task-controller.mjs task-admission.mjs task-view.mjs node-controller.mjs node-settings.mjs local-commands.mjs command-run.mjs graph-model.mjs graph-editor.mjs graph-history.mjs graph-canvas.mjs graph-inspector.mjs graph-menu.mjs graph-editor.css live-editors.mjs
//go:embed command-selection.mjs
//go:embed tool-settings.mjs
var Files embed.FS
