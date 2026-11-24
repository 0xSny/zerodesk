function Stop-ZeroDeskHost([string]$RootDir) {
    $cfgPath = Join-Path $RootDir "data\config.json"
    $httpPort = 9527
    $pin = ""
    if (Test-Path -LiteralPath $cfgPath) {
        try {
            $cfg = Get-Content -LiteralPath $cfgPath -Raw -Encoding UTF8 | ConvertFrom-Json
            if ($cfg.http_port) { $httpPort = [int]$cfg.http_port }
            if ($cfg.pin_code) { $pin = [string]$cfg.pin_code }
        } catch {}
    }

    $running = Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue
    if ($running -and $pin) {
        try {
            $body = (@{ pin = $pin } | ConvertTo-Json -Compress)
            Invoke-RestMethod -Uri ("http://127.0.0.1:{0}/api/admin/shutdown" -f $httpPort) -Method Post -ContentType "application/json" -Body $body -TimeoutSec 2 | Out-Null
            for ($i = 0; $i -lt 10; $i++) {
                Start-Sleep -Milliseconds 150
                if (-not (Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue)) { break }
            }
        } catch {}
    }

    if (Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue) {
        Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 250
    }

    if (Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue) {
        $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
        if (-not $isAdmin) {
            Start-Process -FilePath "powershell.exe" -ArgumentList "-NoProfile -Command `"Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue | Stop-Process -Force`"" -Verb RunAs -Wait
            Start-Sleep -Milliseconds 300
        }
    }

    $oldPattern = Join-Path $RootDir ".build_cache\*.old.exe"
    Remove-Item -Path $oldPattern -Force -ErrorAction SilentlyContinue
}
$root = Split-Path -Parent $PSScriptRoot
$had = [bool](Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue)
Stop-ZeroDeskHost -RootDir $root
if (-not (Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue)) {
    if ($had) {
        Write-Host "[完成] zerodesk-host.exe 已停止。" -ForegroundColor Green
    } else {
        Write-Host "[提示] 当前未检测到运行中的 zerodesk-host.exe 进程。"
    }
} else {
    Write-Host "[警告] 部分高权限 zerodesk-host 进程未能停止，请使用管理员权限运行。" -ForegroundColor Yellow
}
