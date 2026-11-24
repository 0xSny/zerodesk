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
$exe = Join-Path $root "zerodesk-host.exe"
if (-not (Test-Path -LiteralPath $exe)) {
    Write-Host "[错误] 未找到 zerodesk-host.exe，请先完成编译！" -ForegroundColor Red
    Read-Host "按回车键关闭窗口"
    exit 1
}

$cfgPath = Join-Path $root "data\config.json"
$httpPort = 9527
$discPort = 9528
if (Test-Path -LiteralPath $cfgPath) {
    try {
        $cfg = Get-Content -LiteralPath $cfgPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($cfg.http_port) { $httpPort = [int]$cfg.http_port }
        if ($cfg.discovery_port) { $discPort = [int]$cfg.discovery_port }
    } catch {}
}

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if ($isAdmin) {
    Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'ZeroDesk-LAN*' } | Remove-NetFirewallRule -ErrorAction SilentlyContinue
    $lanAddrs = @('192.168.0.0/16', '10.0.0.0/8', '172.16.0.0/12', 'LocalSubnet')
    New-NetFirewallRule -DisplayName 'ZeroDesk-LAN-TCP' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $httpPort -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
    New-NetFirewallRule -DisplayName 'ZeroDesk-LAN-UDP' -Direction Inbound -Action Allow -Protocol UDP -LocalPort $discPort -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
    New-NetFirewallRule -DisplayName 'ZeroDesk-LAN' -Direction Inbound -Action Allow -Protocol UDP -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
    Write-Host "[防火墙] 已配置仅限局域网 IP 段的放行规则 ZeroDesk-LAN（兼容公用/专用网络）。"
} else {
    Write-Host "[提示] 当前非管理员权限启动，如手机无法访问请先运行一次【开启开机后台自启.bat】或右键管理员启动。"
}

Stop-ZeroDeskHost -RootDir $root
Set-Location -LiteralPath $root
& $exe
Read-Host "按回车键关闭窗口"
