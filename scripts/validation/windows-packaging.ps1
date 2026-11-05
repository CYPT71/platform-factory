# Offline OCI packaging acceptance. Place this script beside pf-windows-{arm64,amd64}.exe
# and service-linux-arm64, in a dedicated directory or read-only ISO.
# Uses only its own temporary projects; keeps the JSON report in the guest TEMP directory.
$ErrorActionPreference = 'Stop'
$report = @{ os = [Environment]::OSVersion.VersionString; process_arch = $env:PROCESSOR_ARCHITECTURE; scope = 'Windows ARM guest: OCI packaging, verification and tamper rejection; x64 executed under Windows emulation'; trials = @() }
function Invoke-Native($exe, $arguments) {
    $ErrorActionPreference = 'Continue'
    $lines = & $exe @arguments 2>&1
    $code = $LASTEXITCODE
    return @{ arguments = $arguments; exit_code = $code; output = ($lines | Out-String) }
}
foreach ($arch in @('arm64', 'amd64')) {
    $work = Join-Path ([IO.Path]::GetTempPath()) ('pf-win-' + [Guid]::NewGuid().ToString('N'))
    $item = @{ architecture = $arch; passed = $false; commands = @() }
    try {
        New-Item -ItemType Directory -Path $work | Out-Null
        $exe = Join-Path $work 'pf.exe'
        Copy-Item (Join-Path $PSScriptRoot ('pf-windows-' + $arch + '.exe')) $exe
        Copy-Item (Join-Path $PSScriptRoot 'service-linux-arm64') (Join-Path $work 'service')
        $item.pf_sha256 = (Get-FileHash $exe -Algorithm SHA256).Hash
        Set-Content -Path (Join-Path $work 'pf.yaml') -Encoding ASCII -Value "language: compiled`nartifact: service`nplatform: linux/arm64`nimage: local/windows-validation"
        Push-Location $work
        try {
            foreach ($arguments in @(@('project','build','--yes'), @('verify','--format','text','.platform-factory/image'))) {
                $result = Invoke-Native $exe $arguments
                $item.commands += $result
                if ($result.exit_code -ne 0) { throw ('Native command failed: ' + $result.output) }
            }
            $index = Get-Content '.platform-factory/image/index.json' -Raw | ConvertFrom-Json
            $digest = $index.manifests[0].digest.Split(':')[1]
            $blob = Join-Path $work ('.platform-factory/image/blobs/sha256/' + $digest)
            [IO.File]::AppendAllText($blob, "`n")
            $result = Invoke-Native $exe @('verify','.platform-factory/image')
            $item.commands += $result
            if ($result.exit_code -eq 0) { throw 'Tampered manifest accepted' }
            $item.passed = $true
        } finally { Pop-Location }
    } catch { $item.error = $_.ToString() }
    finally {
        if (Test-Path $work) { Remove-Item -LiteralPath $work -Recurse -Force }
        $item.temporary_directory_removed = -not (Test-Path $work)
        $report.trials += $item
        $report | ConvertTo-Json -Depth 12 | Set-Content -Encoding UTF8 (Join-Path $env:TEMP 'pf-local-native-result.json')
    }
}
$report | ConvertTo-Json -Depth 12
foreach ($trial in $report.trials) { Write-Host ('RESULT ' + $trial.architecture + ' passed=' + $trial.passed + ' cleanup=' + $trial.temporary_directory_removed); if ($trial.error) { Write-Host $trial.error } }
Write-Host ('REPORT ' + (Join-Path $env:TEMP 'pf-local-native-result.json'))
