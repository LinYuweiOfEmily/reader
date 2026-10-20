# Reuse a prepared tool/cache directory. Never silently fetch missing packages.
param([Parameter(Mandatory = $true)][string]$CacheRoot)
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'This script requires Windows.' }
$repo = Split-Path $PSScriptRoot -Parent
$CacheRoot = (Resolve-Path -LiteralPath $CacheRoot).Path
$tooling = Join-Path $CacheRoot 'tooling'
$node = (Get-Command node -ErrorAction Stop).Source
$pnpm = Join-Path $tooling 'node_modules/pnpm/bin/pnpm.cjs'
$go = Join-Path $tooling 'go/bin/go.exe'
$desktop = Get-Content -LiteralPath (Join-Path $repo 'apps/desktop/package.json') -Raw | ConvertFrom-Json
$electronVersion = $desktop.devDependencies.electron
$electronZip = Join-Path $tooling "electron-v$electronVersion-win32-x64.zip"
$sevenZip = Get-ChildItem -LiteralPath (Join-Path $CacheRoot 'cache/electron-builder/7zip@1.0.0') -Recurse -Filter 7za.exe | Select-Object -First 1
foreach ($file in @($pnpm, $go, $electronZip, $sevenZip.FullName, (Join-Path $tooling 'nsis-3.0.4.1/Bin/makensis.exe'), (Join-Path $tooling 'nsis-resources-3.4.1/plugins'))) {
  if (!$file -or !(Test-Path -LiteralPath $file)) { throw "Missing cached tool: $file. Prepare the cache before building." }
}
function Invoke-Pnpm {
  & $node $pnpm @args
  if ($LASTEXITCODE -ne 0) { throw "pnpm failed ($LASTEXITCODE): $args" }
}
function Assert-Hash([string]$File, [string]$Expected) {
  if (!(Test-Path -LiteralPath $File) -or (Get-FileHash -LiteralPath $File -Algorithm SHA256).Hash -ne $Expected) { throw "Missing or invalid cached asset: $File" }
}
$savedEnvironment = @{}
$settings = @{
  PATH = "$(Split-Path $go);$(Join-Path $tooling 'node_modules/.bin');$env:PATH"
  GOPATH = (Join-Path $CacheRoot 'cache/gopath')
  GOCACHE = (Join-Path $CacheRoot 'cache/go-build')
  GOPROXY = 'off'
  GOTOOLCHAIN = 'local'
  GOMAXPROCS = '4'
  npm_config_store_dir = (Join-Path $CacheRoot 'cache/pnpm')
  npm_config_offline = 'true'
  ELECTRON_BUILDER_CACHE = (Join-Path $CacheRoot 'cache/electron-builder')
  ELECTRON_BUILDER_NSIS_DIR = (Join-Path $tooling 'nsis-3.0.4.1')
  ELECTRON_BUILDER_NSIS_RESOURCES_DIR = (Join-Path $tooling 'nsis-resources-3.4.1')
  ELECTRON_BUILDER_7ZIP_PATH = $sevenZip.FullName
  READER_ELECTRON_DIST = $electronZip
  CSC_IDENTITY_AUTO_DISCOVERY = 'false'
  NODE_OPTIONS = '--max-old-space-size=8192'
}
Push-Location $repo
try {
  foreach ($key in $settings.Keys) {
    $savedEnvironment[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
    [Environment]::SetEnvironmentVariable($key, $settings[$key], 'Process')
  }
  Invoke-Pnpm install --frozen-lockfile --offline
  $checksums = Get-Content -LiteralPath (Join-Path $repo 'apps/desktop/node_modules/electron/checksums.json') -Raw | ConvertFrom-Json
  Assert-Hash $electronZip $checksums."electron-v$electronVersion-win32-x64.zip"
  Assert-Hash (Join-Path $tooling 'nsis-3.0.4.1.7z') '9877df902530f96357d13a7a31ae2b9df67f48b11ffc9a1700a7c961574ec5fa'
  Assert-Hash (Join-Path $tooling 'nsis-resources-3.4.1.7z') '593a9a92ef958321293ac6a2ee61e64bf1bd543142a5bd6b3d310709cc924103'
  $model = Get-Content -LiteralPath 'apps/processor/model.json' -Raw | ConvertFrom-Json
  Assert-Hash (Join-Path $repo "apps/processor/models/$($model.name)-$($model.revision).onnx") $model.sha256
  & $node scripts/release-source.mjs
  if ($LASTEXITCODE -ne 0) { throw 'Release metadata validation failed.' }
  Invoke-Pnpm typecheck
  Invoke-Pnpm exec vitest run apps/desktop/main/agent-path.test.ts apps/desktop/main/windows-proxy.test.ts apps/desktop/main/windows-proxy-reader.test.ts apps/desktop/main/command-line.test.ts apps/desktop/main/diagnostics.test.ts apps/desktop/main/endnote-discovery.test.ts --maxWorkers=2
  # package includes a full build, staging, NSIS and isolated runtime smoke tests.
  Invoke-Pnpm package
} finally {
  foreach ($key in $savedEnvironment.Keys) { [Environment]::SetEnvironmentVariable($key, $savedEnvironment[$key], 'Process') }
  Pop-Location
}
