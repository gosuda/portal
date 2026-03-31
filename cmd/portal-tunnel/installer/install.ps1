$ErrorActionPreference = "Stop"
$BaseUrl = if ($env:BASE_URL) { $env:BASE_URL } else { "https://github.com/gosuda/portal/releases/latest/download" }
$RelayUrl = if ($env:RELAY_URL) { $env:RELAY_URL } else { "https://your-relay.example.com" }
$OriginalSecurityProtocol = [System.Net.ServicePointManager]::SecurityProtocol
[System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12
$WorkDir = $null
try {
    $Arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($Arch -eq "ARM64") {
        $PortalArch = "arm64"
    } elseif ($Arch -eq "AMD64" -or $Arch -eq "x86_64") {
        $PortalArch = "amd64"
    } else {
        throw "Unsupported architecture: $Arch"
    }

    $BinPathPrefix = if ($env:BIN_PATH_PREFIX) { $env:BIN_PATH_PREFIX.Trim() } else { "" }
    if ($env:BIN_URL) {
        $BinUrl = $env:BIN_URL
    } elseif ([string]::IsNullOrWhiteSpace($BinPathPrefix)) {
        $BinUrl = "$BaseUrl/portal-windows-$PortalArch.exe"
    } else {
        $BinUrl = "$BaseUrl/$BinPathPrefix/windows-$PortalArch"
    }
    $ChecksumUrl = if ($env:CHECKSUM_URL) { $env:CHECKSUM_URL } else { "$BinUrl.sha256" }
    $WorkDir = Join-Path $env:TEMP ("portal-install-" + [Guid]::NewGuid().ToString())
    New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null
    $BinPath = Join-Path $WorkDir "portal.exe"

    Write-Host "Downloading portal (windows/$PortalArch)..."
    Invoke-WebRequest -UseBasicParsing -Uri $BinUrl -OutFile $BinPath

    Write-Host "Verifying SHA256 checksum..."
    $ChecksumResponse = Invoke-WebRequest -UseBasicParsing -Uri $ChecksumUrl
    if ($ChecksumResponse.Content -is [byte[]]) {
        $ChecksumPayload = [System.Text.Encoding]::UTF8.GetString($ChecksumResponse.Content)
    } else {
        $ChecksumPayload = [string]$ChecksumResponse.Content
    }
    $ChecksumMatch = [regex]::Match($ChecksumPayload, '([A-Fa-f0-9]{64})')
    if (-not $ChecksumMatch.Success) {
        throw "Invalid checksum payload from $ChecksumUrl. Expected '<sha256>  <filename>'."
    }

    $ExpectedHash = $ChecksumMatch.Groups[1].Value.ToLowerInvariant()
    $ActualHash = (Get-FileHash -Algorithm SHA256 -Path $BinPath).Hash.ToLowerInvariant()
    if ($ActualHash -ne $ExpectedHash) {
        throw "Checksum mismatch for portal binary."
    }

    $InstallDir = Join-Path $env:LOCALAPPDATA "portal\bin"
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $InstallPath = Join-Path $InstallDir "portal.exe"
    Copy-Item -Force $BinPath $InstallPath
    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $UserEntries = @()
    if (-not [string]::IsNullOrWhiteSpace($UserPath)) {
        $UserEntries = @($UserPath -split ';' | Where-Object { $_ -ne "" })
    }
    if (-not ($UserEntries -contains $InstallDir)) {
        $NewUserPath = if ([string]::IsNullOrWhiteSpace($UserPath)) {
            $InstallDir
        } else {
            "$InstallDir;$UserPath"
        }
        [Environment]::SetEnvironmentVariable("Path", $NewUserPath, "User")
    }

    $SessionEntries = @($env:Path -split ';' | Where-Object { $_ -ne "" })
    if (-not ($SessionEntries -contains $InstallDir)) {
        $env:Path = "$InstallDir;$env:Path"
    }

    Write-Host "Installed portal to $InstallPath"
    Write-Host "Next step:"
    Write-Host "  portal expose 3000 --relays $RelayUrl"
} finally {
    [System.Net.ServicePointManager]::SecurityProtocol = $OriginalSecurityProtocol
    if ($WorkDir -and (Test-Path $WorkDir)) {
        Remove-Item -Recurse -Force $WorkDir
    }
}
