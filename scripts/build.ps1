[CmdletBinding()]
param(
    [string]$Version = "0.3.0"
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $projectRoot "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null

$targets = @(
    @{ OS = "windows"; Arch = "amd64" },
    @{ OS = "windows"; Arch = "arm64" },
    @{ OS = "darwin"; Arch = "amd64" },
    @{ OS = "darwin"; Arch = "arm64" },
    @{ OS = "linux"; Arch = "amd64" },
    @{ OS = "linux"; Arch = "arm64" }
)

$archives = @()
foreach ($target in $targets) {
    $name = "skillctl_${Version}_$($target.OS)_$($target.Arch)"
    $stage = Join-Path $dist $name
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    $binary = Join-Path $stage "skillctl"
    if ($target.OS -eq "windows") { $binary += ".exe" }
    $env:GOOS = $target.OS
    $env:GOARCH = $target.Arch
    go build -trimpath -ldflags "-s -w -X main.version=$Version" -o $binary $projectRoot
    if ($LASTEXITCODE -ne 0) { throw "Build failed for $($target.OS)/$($target.Arch)" }
    Copy-Item (Join-Path $projectRoot "README.md"), (Join-Path $projectRoot "LICENSE") -Destination $stage
    if ($target.OS -eq "windows") {
        $archive = Join-Path $dist "$name.zip"
        Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $archive -Force
    } else {
        $archive = Join-Path $dist "$name.tar.gz"
        tar -czf $archive -C $stage .
        if ($LASTEXITCODE -ne 0) { throw "Archive failed for $name" }
    }
    $archives += $archive
    Remove-Item -Recurse -Force -LiteralPath $stage
}

Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
$lines = foreach ($archive in $archives) {
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
    "$hash  $(Split-Path -Leaf $archive)"
}
Set-Content -LiteralPath (Join-Path $dist "SHA256SUMS") -Value $lines -Encoding utf8NoBOM
