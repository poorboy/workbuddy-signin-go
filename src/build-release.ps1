# build-release.ps1 - cross-compile and package workbuddy-signin release artifacts.
#
# Usage (from any directory):
#   powershell -ExecutionPolicy Bypass -File .\src\build-release.ps1
#   powershell -ExecutionPolicy Bypass -File .\src\build-release.ps1 -Version v0.1.0
#
# Output goes to <repo>\dist:
#   bin\workbuddy-signin-<os>-<arch>[.exe]        raw executables
#   workbuddy-signin-<version>-<os>-<arch>.zip    Windows archives
#   workbuddy-signin-<version>-<os>-<arch>.tar.gz macOS / Linux archives
#   SHA256SUMS.txt                                checksums
#   BUILD-INFO.txt                                build metadata
#
# UPX is intentionally not used so every target is built the same way.

[CmdletBinding()]
param(
    [string]$Version = "v0.1.0"
)

$ErrorActionPreference = "Stop"

$src = $PSScriptRoot
$root = Split-Path -Parent $src
$dist = Join-Path $root "dist"
$binDir = Join-Path $dist "bin"
$pkgDir = Join-Path $dist "pkg"

$targets = @(
    [pscustomobject]@{ OS = "windows"; Arch = "amd64"; Label = "windows-amd64"; Ext = ".exe"; GOARM = "" },
    [pscustomobject]@{ OS = "windows"; Arch = "arm64"; Label = "windows-arm64"; Ext = ".exe"; GOARM = "" },
    [pscustomobject]@{ OS = "darwin";  Arch = "amd64"; Label = "darwin-amd64";  Ext = "";     GOARM = "" },
    [pscustomobject]@{ OS = "darwin";  Arch = "arm64"; Label = "darwin-arm64";  Ext = "";     GOARM = "" },
    [pscustomobject]@{ OS = "linux";   Arch = "amd64"; Label = "linux-amd64";   Ext = "";     GOARM = "" },
    [pscustomobject]@{ OS = "linux";   Arch = "arm64"; Label = "linux-arm64";   Ext = "";     GOARM = "" },
    [pscustomobject]@{ OS = "linux";   Arch = "arm";   Label = "linux-armv7";   Ext = "";     GOARM = "7" }
)

$go = (Get-Command go -ErrorAction Stop).Source

# Windows' bundled bsdtar does not store Unix execute permissions. Prefer GNU tar
# from Git for Windows so Unix archives are usable without an extra chmod.
$gnuTar = $null
$tarCandidates = @()
$tarCommand = Get-Command tar -ErrorAction SilentlyContinue
if ($tarCommand) { $tarCandidates += $tarCommand.Source }
if ($env:ProgramFiles) { $tarCandidates += (Join-Path $env:ProgramFiles "Git\usr\bin\tar.exe") }
foreach ($candidate in $tarCandidates) {
    if (-not $candidate -or -not (Test-Path $candidate)) { continue }
    $firstLine = (& $candidate --version 2>$null | Select-Object -First 1)
    if ($firstLine -match "GNU tar") { $gnuTar = $candidate; break }
}

if (Test-Path $dist) { Remove-Item $dist -Recurse -Force }
New-Item -ItemType Directory -Force -Path $binDir, $pkgDir | Out-Null

Write-Host "workbuddy-signin release build"
Write-Host "  version : $Version"
Write-Host "  go      : $(& $go version)"
if ($gnuTar) { Write-Host "  tar     : $gnuTar" }
else { Write-Warning "GNU tar not found; Unix packages will fall back to .zip (run chmod +x after extracting)." }
Write-Host ""

$env:CGO_ENABLED = "0"
Push-Location $src
try {
    foreach ($target in $targets) {
        $env:GOOS = $target.OS
        $env:GOARCH = $target.Arch
        if ($target.GOARM) { $env:GOARM = $target.GOARM }
        else { Remove-Item Env:GOARM -ErrorAction SilentlyContinue }

        $label = $target.Label
        $binName = "workbuddy-signin$($target.Ext)"
        $binPath = Join-Path $binDir ("workbuddy-signin-$label$($target.Ext)")

        Write-Host "[build] $label"
        & $go build -trimpath -buildvcs=false "-gcflags=all=-l" "-ldflags=-s -w -buildid=" -o $binPath .
        if ($LASTEXITCODE -ne 0) { throw "go build failed: $label" }

        $stage = Join-Path $pkgDir "workbuddy-signin-$label"
        New-Item -ItemType Directory -Force -Path $stage | Out-Null
        Copy-Item $binPath (Join-Path $stage $binName)
        Copy-Item (Join-Path $root "README.md") $stage
        Copy-Item (Join-Path $root "LICENSE") $stage

        $baseName = "workbuddy-signin-$Version-$label"
        if ($target.OS -eq "windows" -or -not $gnuTar) {
            $archive = Join-Path $dist "$baseName.zip"
            Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $archive -CompressionLevel Optimal -Force
        } else {
            # GNU tar --mode is global, so build the tar in two passes to keep the
            # binary executable and the docs non-executable.
            $tarName = "$baseName.tar"
            $archive = Join-Path $dist "$baseName.tar.gz"
            $stageRel = "pkg/workbuddy-signin-$label"
            Push-Location $dist
            try {
                & $gnuTar -cf $tarName --mode=0755 -C $stageRel $binName
                if ($LASTEXITCODE -ne 0) { throw "tar failed: $label" }
                & $gnuTar -rf $tarName --mode=0644 -C $stageRel "README.md" "LICENSE"
                if ($LASTEXITCODE -ne 0) { throw "tar append failed: $label" }
            } finally {
                Pop-Location
            }

            $tarPath = Join-Path $dist $tarName
            $input = [IO.File]::OpenRead($tarPath)
            try {
                $output = [IO.File]::Create($archive)
                try {
                    $gzip = New-Object IO.Compression.GZipStream($output, [IO.Compression.CompressionMode]::Compress)
                    try { $input.CopyTo($gzip) } finally { $gzip.Dispose() }
                } finally { $output.Dispose() }
            } finally { $input.Dispose() }
            Remove-Item $tarPath -Force
        }
        Write-Host ("        -> {0} ({1:N0} bytes)" -f (Split-Path -Leaf $archive), (Get-Item $archive).Length)
    }
} finally {
    Pop-Location
    Remove-Item Env:GOOS, Env:GOARCH, Env:GOARM, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}

# Checksums: raw binaries plus release archives.
$files = @()
$files += Get-ChildItem $binDir -File
$files += Get-ChildItem $dist -File | Where-Object { $_.Name -match "\.(zip|tar\.gz)$" }
$lines = foreach ($file in ($files | Sort-Object FullName)) {
    $relative = $file.FullName.Substring($dist.Length + 1).Replace("\", "/")
    $hash = (Get-FileHash $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $relative"
}
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText((Join-Path $dist "SHA256SUMS.txt"), (($lines -join "`n") + "`n"), $utf8NoBom)

# Build metadata.
$commit = (& git -C $root rev-parse --short HEAD 2>$null)
if (-not $commit) { $commit = "unknown" }
$status = (& git -C $root status --porcelain 2>$null)
if ($status) { $commit += "-dirty" }

$info = @(
    "name: workbuddy-signin",
    "version: $Version",
    "commit: $commit",
    "built_at: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss zzz')",
    "go: $(& $go version)",
    "flags: -trimpath -buildvcs=false -gcflags=all=-l -ldflags=-s -w -buildid=",
    "targets: $($targets.Count)",
    ""
) + ($targets | ForEach-Object { "  $($_.Label)" })
[IO.File]::WriteAllText((Join-Path $dist "BUILD-INFO.txt"), (($info -join "`n") + "`n"), $utf8NoBom)

Remove-Item $pkgDir -Recurse -Force

Write-Host ""
Write-Host "Done: $dist"
Get-ChildItem $dist -File | Sort-Object Name | ForEach-Object {
    Write-Host ("  {0,-58} {1:N0} bytes" -f $_.Name, $_.Length)
}
