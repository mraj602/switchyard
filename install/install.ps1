# Watchyard installer for Windows (docs/spec/setup.md 1.3). PowerShell 5.1+.
#
#   irm https://github.com/mraj602/watchyard/releases/latest/download/install.ps1 | iex
#   & ([scriptblock]::Create((irm https://github.com/mraj602/watchyard/releases/latest/download/install.ps1))) -Version 0.3.0 -NoSetup
#
# Puts a checksum-verified wy.exe in $HOME\.local\bin and nothing else (PATH is not changed).
# Then runs `wy setup` unless -NoSetup or the host is non-interactive.
# When the gh CLI is installed it also verifies the archive's build attestation (who built it);
# without gh only the checksum is checked, which shows the download is intact but not its origin.
#
# Exit codes: 0 ok, 2 unsupported or usage, 3 verification failed, 4 download failed.
# Environment: WY_INSTALL_BASE_URL (mirror/tests; https, or http on a loopback host only),
# WY_VERIFY_ATTESTATION=1 (fail when gh is missing) or =0 (do not check the attestation).
param(
    [string]$Version = 'latest',
    [string]$BinDir = '',
    [switch]$NoSetup,
    [string[]]$SetupArgs = @()
)

function Install-Sy {
    param([string]$Version, [string]$BinDir, [bool]$NoSetup, [string[]]$SetupArgs)

    $ErrorActionPreference = 'Stop'
    $owner = 'mraj602'
    $repo = 'watchyard'
    # The repository's name before a rename (docs/spec/rename.md 2.7); equal to $repo without one.
    $legacyRepo = 'switchyard' # rename:keep
    # The command's name before a rename (docs/spec/rename.md 2.13): its variables still apply
    # and its binary becomes a copy of the new one. Equal to wy without one.
    $legacyCli = 'sy' # rename:keep
    if ($legacyCli -ne 'wy') {
        foreach ($v in @('INSTALL_BASE_URL', 'VERIFY_ATTESTATION')) {
            $cur = [Environment]::GetEnvironmentVariable("WY_$v")
            $old = [Environment]::GetEnvironmentVariable($legacyCli.ToUpper() + "_$v")
            if (-not $cur -and $old) { [Environment]::SetEnvironmentVariable("WY_$v", $old) }
        }
    }
    $base = $env:WY_INSTALL_BASE_URL
    if (-not $base) { $base = "https://github.com/$owner/$repo/releases" }
    $base = $base.TrimEnd('/')
    # https only; plain http just to a loopback host (the tests' local server).
    $baseUri = $null
    if (-not [Uri]::TryCreate($base, [UriKind]::Absolute, [ref]$baseUri) -or $baseUri.UserInfo) {
        [Console]::Error.WriteLine("install.ps1: WY_INSTALL_BASE_URL must be an https URL without credentials"); return 2
    }
    if ($baseUri.Scheme -ne 'https' -and -not ($baseUri.Scheme -eq 'http' -and $baseUri.IsLoopback)) {
        [Console]::Error.WriteLine("install.ps1: WY_INSTALL_BASE_URL must be https (http is allowed only for a loopback host)"); return 2
    }

    if ($Version -ne 'latest') { $Version = $Version.TrimStart('v') }
    if ($Version -notmatch '^[0-9A-Za-z.+-]+\z') {
        [Console]::Error.WriteLine("install.ps1: bad version: $Version"); return 2
    }
    if (-not $BinDir) { $BinDir = Join-Path $HOME '.local\bin' }

    # Architecture (the OS architecture, not the one of a 32-bit host process).
    $procArch = $env:PROCESSOR_ARCHITEW6432
    if (-not $procArch) { $procArch = $env:PROCESSOR_ARCHITECTURE }
    switch ($procArch) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { [Console]::Error.WriteLine("install.ps1: unsupported architecture $procArch"); return 2 }
    }

    if ($Version -eq 'latest') { $dl = "$base/latest/download" } else { $dl = "$base/download/v$Version" }

    # Windows PowerShell 5.1 defaults to TLS 1.0/1.1 on some systems: allow TLS 1.2 and newer only.
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch { }
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]12288 } catch { }
    $ProgressPreference = 'SilentlyContinue'

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("sy-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$dl/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS')
        } catch {
            [Console]::Error.WriteLine("install.ps1: download failed: $dl/SHA256SUMS"); return 4
        }
        $sums = @(Get-Content (Join-Path $tmp 'SHA256SUMS') | Where-Object { $_.Trim() })

        function Find-Line([string]$name) {
            foreach ($l in $sums) {
                $parts = $l.Trim() -split '\s+', 2
                if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $name) { return $parts[0] }
            }
            return $null
        }

        if ($Version -eq 'latest') {
            $suffix = "_windows_$arch.zip"
            $archive = $null
            foreach ($l in $sums) {
                $fields = $l.Trim() -split '\s+', 2
                if ($fields.Count -ne 2) { continue }   # malformed line: skip it
                $n = $fields[1].TrimStart('*')
                if ($n.StartsWith('wy_') -and $n.EndsWith($suffix)) { $archive = $n; break }
            }
            if (-not $archive) {
                [Console]::Error.WriteLine("install.ps1: SHA256SUMS lists no archive for windows_$arch"); return 3
            }
            $Version = $archive.Substring(3, $archive.Length - 3 - $suffix.Length)
        } else {
            $archive = "wy_${Version}_windows_$arch.zip"
        }

        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$dl/$archive" -OutFile (Join-Path $tmp $archive)
        } catch {
            [Console]::Error.WriteLine("install.ps1: download failed: $dl/$archive"); return 4
        }

        $want = Find-Line $archive
        if (-not $want) {
            [Console]::Error.WriteLine("install.ps1: $archive is not listed in SHA256SUMS; nothing installed"); return 3
        }
        $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $archive)).Hash
        if ($got.ToLower() -ne $want.ToLower()) {
            [Console]::Error.WriteLine("install.ps1: checksum mismatch for $archive; nothing installed"); return 3
        }

        # The build attestation shows the archive was built by this repository's release
        # workflow, which a checksum from the same release cannot. Checked whenever gh is installed.
        $proof = 'checksum ok'
        $note = $null
        if ($env:WY_VERIFY_ATTESTATION -eq '0') {
            $note = 'build attestation not checked (WY_VERIFY_ATTESTATION=0); the checksum shows the download is intact, not who built it'
        } elseif (Get-Command gh -ErrorAction SilentlyContinue) {
            # 5.1 turns native stderr into an error record under Stop; any failure here is exit 3.
            $ok = $false
            foreach ($r in @($repo, $legacyRepo | Select-Object -Unique)) {
                try {
                    & gh attestation verify (Join-Path $tmp $archive) --repo "$owner/$r" --signer-workflow "$owner/$r/.github/workflows/release.yml" 2>&1 | Out-Null
                    $ok = ($LASTEXITCODE -eq 0)
                } catch { $ok = $false }
                if ($ok) { break }
            }
            if (-not $ok) {
                [Console]::Error.WriteLine("install.ps1: attestation verification failed for $archive; nothing installed (gh must be signed in and reach github.com; WY_VERIFY_ATTESTATION=0 skips this check)"); return 3
            }
            $proof = 'checksum ok, build attestation ok'
        } elseif ($env:WY_VERIFY_ATTESTATION -eq '1') {
            [Console]::Error.WriteLine("install.ps1: WY_VERIFY_ATTESTATION=1 needs the gh CLI, which is not installed; nothing installed"); return 3
        } else {
            $note = 'build attestation not checked because the gh CLI is not installed; the checksum shows the download is intact, not who built it. Install gh (https://cli.github.com) and run the installer again to check it'
        }

        Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath (Join-Path $tmp 'x') -Force
        $exe = Join-Path $tmp 'x\wy.exe'
        if (-not (Test-Path $exe)) {
            [Console]::Error.WriteLine("install.ps1: $archive does not contain wy.exe"); return 3
        }

        New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
        $dest = Join-Path $BinDir 'wy.exe'
        $new = Join-Path $BinDir ('wy.exe.' + [Guid]::NewGuid().ToString('N') + '.new')
        [IO.File]::Copy($exe, $new, $false) # fails if the name exists: an exclusive temp file
        $moved = $false
        for ($i = 0; $i -lt 5 -and -not $moved; $i++) {
            try { Move-Item -Force -Path $new -Destination $dest; $moved = $true }
            catch { Start-Sleep -Milliseconds 400 }
        }
        if (-not $moved) {
            Remove-Item -Force -ErrorAction SilentlyContinue $new
            [Console]::Error.WriteLine("install.ps1: cannot replace $dest (in use?)"); return 4
        }
        $legacyExe = Join-Path $BinDir ($legacyCli + '.exe')
        if ($legacyCli -ne 'wy' -and (Test-Path -PathType Leaf $legacyExe)) {
            # Hooks, tasks and launchers written for the old name keep working.
            try { Copy-Item -Force -Path $dest -Destination $legacyExe } catch { Write-Host "note: $legacyExe is in use and stays as it was" }
        }
    } finally {
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmp
    }

    Write-Host "wy $Version downloaded, $proof, installed to $dest"
    if ($note) { Write-Host "note: $note" }
    $onPath = ($env:PATH -split ';') | Where-Object { $_.TrimEnd('\') -eq $BinDir.TrimEnd('\') }
    if (-not $onPath) {
        Write-Host "$BinDir is not on your PATH. Add it:"
        Write-Host "  [Environment]::SetEnvironmentVariable('Path', `$env:Path + ';$BinDir', 'User')"
    }

    if ($NoSetup) { return 0 }
    if ([Environment]::UserInteractive -and -not [Console]::IsInputRedirected) {
        # Out-Host: the wizard's output must not become part of this function's result.
        & $dest setup @SetupArgs | Out-Host
        $setupCode = $LASTEXITCODE
        return $setupCode
    }
    Write-Host 'run: wy setup'
    return 0
}

$code = Install-Sy -Version $Version -BinDir $BinDir -NoSetup ([bool]$NoSetup) -SetupArgs $SetupArgs
$global:LASTEXITCODE = $code
# `exit` would close the caller's session when this text is run through iex; only exit when
# it runs as a script file.
if ($MyInvocation.MyCommand.Path) { exit $code }
