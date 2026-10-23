param([string]$Version = '1.0.0')
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "Version must be major.minor.patch (got '$Version')." }
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (!$go) { throw 'Go is required: https://go.dev/dl/' }
$root = $PSScriptRoot
$dist = Join-Path $root 'dist'
$parts = @($Version.Split('.') | ForEach-Object { [int]$_ }) + 0
$full = $parts -join '.'
# Pin everything that shapes the output, so one tag always builds the same
# EXE: the toolchain named in go.mod, the target, and no cgo.
$toolchain = (Select-String -LiteralPath (Join-Path $root 'go.mod') -Pattern '^toolchain (go\S+)').Matches[0].Groups[1].Value
$pinned = @{ GOTOOLCHAIN = $toolchain; GOOS = 'windows'; GOARCH = 'amd64'; GOAMD64 = 'v1'; CGO_ENABLED = '0' }
$saved = @{}
foreach ($key in $pinned.Keys) {
    $saved[$key] = [Environment]::GetEnvironmentVariable($key)
    [Environment]::SetEnvironmentVariable($key, $pinned[$key])
}
$manifest = [IO.Path]::GetTempFileName()
Push-Location -LiteralPath $root
try {
    # Embed the icon, the administrator manifest and the version resource. Line
    # endings are normalised so checkout settings cannot change the EXE.
    $text = (Get-Content -LiteralPath assets/app.manifest -Raw) -replace "`r`n", "`n" -replace '(<assemblyIdentity version=")[0-9.]+(")', "`${1}$full`${2}"
    [IO.File]::WriteAllText($manifest, $text)  # UTF-8 without BOM
    & $go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1 -64 -manifest $manifest -icon assets/app.ico -o cmd/opencw/rsrc_windows_amd64.syso `
        -ver-major $parts[0] -ver-minor $parts[1] -ver-patch $parts[2] -ver-build $parts[3] `
        -product-ver-major $parts[0] -product-ver-minor $parts[1] -product-ver-patch $parts[2] -product-ver-build $parts[3] `
        -file-version $full -product-version $Version assets/versioninfo.json
    if ($LASTEXITCODE -ne 0) { throw 'Resource embedding failed.' }
    & $go build -trimpath -buildvcs=false -tags with_utls,with_quic -ldflags "-s -w -H windowsgui -X main.appVersion=$Version" -o (Join-Path $dist 'OpenCW.exe') ./cmd/opencw
    if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
} finally {
    Pop-Location
    Remove-Item -LiteralPath $manifest -ErrorAction SilentlyContinue
    foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key]) }
}
# Ship the user guide, the license and the third-party notices with the EXE.
Copy-Item -LiteralPath (Join-Path $root 'assets/README.txt') -Destination $dist -Force
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $dist 'LICENSE.txt') -Force
Copy-Item -LiteralPath (Join-Path $root 'THIRD_PARTY_NOTICES.md') -Destination $dist -Force
Get-Item -LiteralPath (Join-Path $dist 'OpenCW.exe') | Select-Object FullName, Length
