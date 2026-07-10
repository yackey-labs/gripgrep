# gripgrep installer / updater for Windows.
#
#   irm https://raw.githubusercontent.com/yackey-labs/gripgrep/main/install.ps1 | iex
#
# Re-running updates in place: the new gg.exe is downloaded and
# checksum-verified first; the running/old binary is renamed aside
# (allowed even while in use), the new one moved in, and the old one
# deleted. Also drops a `gg-update.ps1` helper next to gg.exe.
#
# Environment:
#   GG_INSTALL_DIR   install directory (default: $env:LOCALAPPDATA\Programs\gg)
#   GG_VERSION       specific tag to install (default: latest release)
$ErrorActionPreference = "Stop"

$Repo = "yackey-labs/gripgrep"
$RawUrl = "https://raw.githubusercontent.com/$Repo/main/install.ps1"
$InstallDir = if ($env:GG_INSTALL_DIR) { $env:GG_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\gg" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "amd64" }
  "ARM64" { "arm64" }
  default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$Version = $env:GG_VERSION
if (-not $Version) {
  $latest = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
  $Version = $latest.tag_name
  if (-not $Version) { throw "could not determine the latest release — is one published yet? https://github.com/$Repo/releases" }
}

$Exe = Join-Path $InstallDir "gg.exe"
if (Test-Path $Exe) {
  $current = & $Exe --version 2>$null
  if ($current -match [regex]::Escape($Version)) {
    Write-Host "gg $Version is already installed at $Exe"
    return
  }
}

$Name = "gg-$Version-windows-$arch"
$Zip = "$Name.zip"
$Tmp = Join-Path ([IO.Path]::GetTempPath()) "gg-install-$PID"
New-Item -ItemType Directory -Force -Path $Tmp | Out-Null
try {
  Write-Host "downloading gg $Version (windows/$arch)..."
  Invoke-WebRequest -Uri "https://github.com/$Repo/releases/download/$Version/$Zip" -OutFile (Join-Path $Tmp $Zip)

  try {
    Invoke-WebRequest -Uri "https://github.com/$Repo/releases/download/$Version/SHA256SUMS" -OutFile (Join-Path $Tmp "SHA256SUMS")
    $expected = (Get-Content (Join-Path $Tmp "SHA256SUMS") | Where-Object { $_ -match [regex]::Escape($Zip) }) -split '\s+' | Select-Object -First 1
    $actual = (Get-FileHash (Join-Path $Tmp $Zip) -Algorithm SHA256).Hash.ToLower()
    if ($expected -and ($expected.ToLower() -ne $actual)) { throw "checksum verification FAILED" }
    if ($expected) { Write-Host "checksum OK" }
  } catch [System.Net.WebException] {
    Write-Host "no checksum file for this release; skipping verification"
  }

  Expand-Archive -Path (Join-Path $Tmp $Zip) -DestinationPath $Tmp -Force
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

  # Safe replace: renaming a running exe is allowed; overwriting isn't.
  $Old = "$Exe.old"
  if (Test-Path $Exe) {
    Remove-Item $Old -Force -ErrorAction SilentlyContinue
    Move-Item $Exe $Old -Force
  }
  Move-Item (Join-Path $Tmp "$Name\gg.exe") $Exe -Force
  Remove-Item $Old -Force -ErrorAction SilentlyContinue

  # The update command: re-runs this installer.
  @"
# Updates gg by re-running the gripgrep installer.
irm $RawUrl | iex
"@ | Set-Content (Join-Path $InstallDir "gg-update.ps1")

  Write-Host "installed: $Exe (update any time with gg-update.ps1)"
  & $Exe --version

  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  if ($userPath -notlike "*$InstallDir*") {
    Write-Host "note: $InstallDir is not on your PATH — add it with:"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', `"$userPath;$InstallDir`", 'User')"
  }
} finally {
  Remove-Item $Tmp -Recurse -Force -ErrorAction SilentlyContinue
}
