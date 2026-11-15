package executor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func resolvePinnedRuntimeForPreflight(t *testing.T) (string, string) {
	t.Helper()
	node := os.Getenv("ASTERHAND_CHAT_NODE")
	entry := os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node != "" && entry != "" {
		absNode, err1 := filepath.Abs(node)
		absEntry, err2 := filepath.Abs(entry)
		if err1 == nil && err2 == nil {
			return absNode, absEntry
		}
		return node, entry
	}
	matches, _ := filepath.Glob(filepath.Join("..", "..", "out", "local-install", "dependencies-*"))
	for _, base := range matches {
		candNode, err1 := filepath.Abs(filepath.Join(base, "node-distribution", "node-v24.22.1-win-x64", "node.exe"))
		candEntry, err2 := filepath.Abs(filepath.Join(base, "runtime", "node_modules", "openclaw", "openclaw.mjs"))
		if err1 != nil || err2 != nil {
			continue
		}
		if _, err1 := os.Stat(candNode); err1 == nil {
			if _, err2 := os.Stat(candEntry); err2 == nil {
				return candNode, candEntry
			}
		}
	}
	t.Skip("pinned OpenClaw + Node bundle not available for SDK preflight")
	return "", ""
}

const sdkPreflightScript = `
import {registerHooks} from 'node:module';
import {pathToFileURL} from 'node:url';
import path from 'node:path';

const [runtimeDir, pluginDir, workDir] = process.argv.slice(2);
const runtimeParentURL = pathToFileURL(path.join(runtimeDir, 'package.json')).href;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === 'openclaw' || specifier.startsWith('openclaw/')) {
      return nextResolve(specifier, {...context, parentURL: runtimeParentURL});
    }
    return nextResolve(specifier, context);
  },
});

const {createOpenClawCodingTools} = await import('openclaw/plugin-sdk/agent-harness');
const {applyEmbeddedAttemptToolsAllow, toolPolicy} = await import('openclaw/plugin-sdk/agent-harness-runtime');
const pluginMod = await import(pathToFileURL(path.join(pluginDir, 'index.mjs')).href);
const filesMod = await import(pathToFileURL(path.join(pluginDir, 'files.mjs')).href);
const displayMod = await import(pathToFileURL(path.join(pluginDir, 'tool-display.mjs')).href);

const registered = [];
const hooks = [];
const allowed = [...filesMod.allowedTools];
const denyList = ['exec', 'process', 'code_execution', 'skills_read', 'memory-core'];
const api = {
  config: {
    tools: {
      allow: allowed,
      deny: denyList,
      updatePlan: false,
      fs: {workspaceOnly: true},
      exec: {applyPatch: {enabled: true, workspaceOnly: true}},
      codeMode: {enabled: false},
      toolSearch: false,
      sandbox: {tools: {allow: allowed}},
    },
  },
  pluginConfig: {
    workspace: workDir,
    endpoint: 'http://127.0.0.1:65530',
    token: 'preflight-token',
    policy: 'preflight-policy',
  },
  registerTool(tool) {
    registered.push(tool);
  },
  on(name) {
    hooks.push(name);
  },
};

pluginMod.default.register(api);

const dummyBridge = {
  pathMappings: [{hostRoot: workDir, containerRoot: workDir}],
  resolvePath: ({filePath}) => ({hostPath: filePath, containerPath: filePath, relativePath: filePath}),
  readDirectory: async () => [],
};
const coreSurface = createOpenClawCodingTools({
  workspaceDir: workDir,
  modelProvider: 'openai',
  wrapBeforeToolCallHook: false,
  config: {
    tools: {
      updatePlan: false,
      fs: {workspaceOnly: true},
      exec: {applyPatch: {enabled: true, workspaceOnly: true}},
    },
  },
  sandbox: {
    enabled: true,
    workspaceDir: workDir,
    agentWorkspaceDir: workDir,
    containerWorkdir: workDir,
    workspaceAccess: 'rw',
    docker: {},
    fsBridge: dummyBridge,
  },
  toolConstructionPlan: {
    includeBaseCodingTools: true,
    includeShellTools: true,
    includeChannelTools: true,
    includeOpenClawTools: true,
    includePluginTools: false,
  },
});

const coreNames = [...new Set(coreSurface.map(t => t.name.toLowerCase()))];
const nonCoreMatcher = toolPolicy.createToolPolicyMatcher({deny: coreNames}, false);
const checkCollisions = names => names.filter(name => coreNames.includes(name.toLowerCase()) || !nonCoreMatcher(name));

const pluginSet = new Set(registered);
const runtimePolicy = toolPolicy.createToolPolicyMatcher({allow: allowed, deny: denyList}, false);
const combinedSurface = [...coreSurface, ...registered].filter(t => runtimePolicy(t.name));
const effectiveSurface = applyEmbeddedAttemptToolsAllow(combinedSurface, allowed);

const displayErrors = registered.flatMap(t => {
  const res = displayMod.validateToolDisplaySpec(t);
  return res.ok ? [] : res.errors;
});
const negativeDisplayCaught =
  !displayMod.validateToolDisplaySpec({name: 'no_spec', parameters: {properties: {path: {}}}}).ok &&
  !displayMod.validateToolDisplaySpec({name: 'read_file', display: {kind: 'path', pathParams: ['missing_field']}, parameters: {properties: {path: {}}}}).ok;

console.log(JSON.stringify({
  allowedTools: allowed,
  registeredNames: registered.map(t => t.name),
  hasDescriptions: registered.every(t => typeof t.description === 'string' && t.description.length > 0 && t.parameters?.type === 'object'),
  displayErrors,
  negativeDisplayCaught,
  hooks,
  collisions: checkCollisions(registered.map(t => t.name)),
  simulatedCollisions: checkCollisions(['read', 'apply_patch', 'apply-patch', 'bash']),
  effectiveNames: effectiveSurface.map(t => t.name),
  allOwnedByPlugin: effectiveSurface.length === registered.length && effectiveSurface.every(t => pluginSet.has(t)),
}));
`

func TestWorkspacePluginSDKPreflight(t *testing.T) {
	manifestRaw, err := workspacePluginFiles.ReadFile("workspaceplugin/openclaw.plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		ID        string `json:"id"`
		Contracts struct {
			Tools []string `json:"tools"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	wantTools := nativeWorkspaceTools()
	if manifest.ID != workspacePlugin {
		t.Fatalf("manifest id = %q, want %q", manifest.ID, workspacePlugin)
	}
	if !reflect.DeepEqual(manifest.Contracts.Tools, wantTools) {
		t.Fatalf("manifest contracts.tools = %v, want %v", manifest.Contracts.Tools, wantTools)
	}

	node, entry := resolvePinnedRuntimeForPreflight(t)
	runtimeDir := filepath.Dir(filepath.Dir(filepath.Dir(entry)))
	tempRoot := t.TempDir()
	if err := prepareWorkspacePlugin(tempRoot); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(tempRoot, "workspace-plugin")
	workDir := filepath.Join(tempRoot, "workspace")
	if err := os.MkdirAll(workDir, 0700); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(tempRoot, "preflight.mjs")
	if err := os.WriteFile(scriptPath, []byte(sdkPreflightScript), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, scriptPath, runtimeDir, pluginDir, workDir)
	cmd.Dir = runtimeDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK preflight failed: %v\n%s", err, out)
	}
	var report struct {
		AllowedTools        []string `json:"allowedTools"`
		RegisteredNames       []string `json:"registeredNames"`
		HasDescriptions       bool     `json:"hasDescriptions"`
		DisplayErrors         []string `json:"displayErrors"`
		NegativeDisplayCaught bool     `json:"negativeDisplayCaught"`
		Hooks                 []string `json:"hooks"`
		Collisions          []string `json:"collisions"`
		SimulatedCollisions []string `json:"simulatedCollisions"`
		EffectiveNames      []string `json:"effectiveNames"`
		AllOwnedByPlugin    bool     `json:"allOwnedByPlugin"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("invalid preflight report: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(report.AllowedTools, wantTools) {
		t.Fatalf("allowedTools = %v, want %v", report.AllowedTools, wantTools)
	}
	if !reflect.DeepEqual(report.RegisteredNames, wantTools) {
		t.Fatalf("registeredNames = %v, want %v", report.RegisteredNames, wantTools)
	}
	if !reflect.DeepEqual(report.EffectiveNames, wantTools) {
		t.Fatalf("effectiveNames after OpenClaw policy filter = %v, want %v", report.EffectiveNames, wantTools)
	}
	if !report.AllOwnedByPlugin {
		t.Fatal("policy-filtered effective surface included non-plugin core tools")
	}
	if !report.HasDescriptions {
		t.Fatal("one or more registered tools lacked description or object parameters schema")
	}
	if len(report.DisplayErrors) != 0 {
		t.Fatalf("tool display spec validation errors: %v", report.DisplayErrors)
	}
	if !report.NegativeDisplayCaught {
		t.Fatal("validateToolDisplaySpec failed to reject missing or mismatched display spec")
	}
	if len(report.Collisions) != 0 {
		t.Fatalf("plugin tool names collided with OpenClaw core tools or aliases: %v", report.Collisions)
	}
	for _, wantCollision := range []string{"read", "apply_patch", "apply-patch", "bash"} {
		if !slices.Contains(report.SimulatedCollisions, wantCollision) {
			t.Fatalf("collision detector failed to flag simulated core/alias collision %q: %v", wantCollision, report.SimulatedCollisions)
		}
	}
	for _, requiredHook := range []string{"before_prompt_build", "before_tool_call", "after_tool_call", "gateway_stop"} {
		if !slices.Contains(report.Hooks, requiredHook) {
			t.Fatalf("missing plugin hook %s in %v", requiredHook, report.Hooks)
		}
	}
}
