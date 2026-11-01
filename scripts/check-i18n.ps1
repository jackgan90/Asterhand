#requires -Version 7.0
[CmdletBinding()]
param([string]$Go = 'go', [string]$Node = 'node')
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot

& $Node (Join-Path $PSScriptRoot 'sync-i18n.mjs') --check
if ($LASTEXITCODE -ne 0) { throw 'Frontend i18n catalog or template/JS guardrails failed.' }

& $Go -C $repo test -count=1 ./internal/i18n
if ($LASTEXITCODE -ne 0) { throw 'Go i18n AST or full-stack catalog guardrails failed.' }

& $Node --test (Join-Path $repo 'tests/i18n-guard.test.mjs')
if ($LASTEXITCODE -ne 0) { throw 'Node i18n guardrail contract tests failed.' }
