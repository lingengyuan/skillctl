[CmdletBinding()]
param(
    [string]$Version,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$installDir = Join-Path $env:LOCALAPPDATA "Programs\skillctl\bin"
$binary = Join-Path $installDir "skillctl.exe"

function Update-UserPath([bool]$Add) {
    $current = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @($current -split ";" | Where-Object { $_ })
    $exists = $parts | Where-Object { [string]::Equals($_.TrimEnd("\"), $installDir.TrimEnd("\"), [StringComparison]::OrdinalIgnoreCase) }
    if ($Add -and -not $exists) { $parts += $installDir }
    if (-not $Add) { $parts = @($parts | Where-Object { -not [string]::Equals($_.TrimEnd("\"), $installDir.TrimEnd("\"), [StringComparison]::OrdinalIgnoreCase) }) }
    [Environment]::SetEnvironmentVariable("Path", ($parts -join ";"), "User")
}

if ($Uninstall) {
    if (Test-Path -LiteralPath $binary) { Remove-Item -Force -LiteralPath $binary }
    Update-UserPath $false
    Write-Host "skillctl was uninstalled. Configuration and skills were preserved."
    exit 0
}

if (-not $Version) {
    $release = Invoke-RestMethod "https://api.github.com/repos/lingengyuan/skillctl/releases/latest"
    $Version = $release.tag_name
}
$plainVersion = $Version.TrimStart("v")
$architecture = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    "X64" { "amd64" }
    "Arm64" { "arm64" }
    default { throw "Unsupported architecture: $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
}
$archive = "skillctl_${plainVersion}_windows_${architecture}.zip"
$base = "https://github.com/lingengyuan/skillctl/releases/download/$Version"
$temp = Join-Path ([IO.Path]::GetTempPath()) ("skillctl-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temp | Out-Null
try {
    Invoke-WebRequest "$base/$archive" -OutFile (Join-Path $temp $archive)
    Invoke-WebRequest "$base/SHA256SUMS" -OutFile (Join-Path $temp "SHA256SUMS")
    $expected = (Get-Content (Join-Path $temp "SHA256SUMS") | Where-Object { $_ -match [regex]::Escape($archive) } | Select-Object -First 1) -split "\s+"
    if (-not $expected[0]) { throw "No checksum found for $archive" }
    $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $temp $archive)).Hash
    if (-not [string]::Equals($expected[0], $actual, [StringComparison]::OrdinalIgnoreCase)) { throw "Checksum verification failed" }
    Expand-Archive (Join-Path $temp $archive) -DestinationPath $temp -Force
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Copy-Item (Join-Path $temp "skillctl.exe") $binary -Force
    Update-UserPath $true
    Write-Host "Installed skillctl $Version. Open a new terminal to use it."
} finally {
    if (Test-Path -LiteralPath $temp) { Remove-Item -Recurse -Force -LiteralPath $temp }
}
