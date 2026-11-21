$ErrorActionPreference = 'Stop'

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "正在请求管理员权限以配置防火墙与高权限开机自启..."
    Start-Process -FilePath "powershell.exe" -ArgumentList "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`"" -Verb RunAs
    exit 0
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

Write-Host "[1/3] 配置局域网专用防火墙放行规则 (Profile Any + 私有网段)..."
Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'ZeroDesk-LAN*' } | Remove-NetFirewallRule -ErrorAction SilentlyContinue
$lanAddrs = @('192.168.0.0/16', '10.0.0.0/8', '172.16.0.0/12', 'LocalSubnet')
New-NetFirewallRule -DisplayName 'ZeroDesk-LAN-TCP' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $httpPort -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
New-NetFirewallRule -DisplayName 'ZeroDesk-LAN-UDP' -Direction Inbound -Action Allow -Protocol UDP -LocalPort $discPort -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
New-NetFirewallRule -DisplayName 'ZeroDesk-LAN' -Direction Inbound -Action Allow -Protocol UDP -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null

Write-Host "[2/3] 注册高权限开机自启计划任务 ZeroDesk-AutoStart (-tray 后台托盘模式)..."
Get-ScheduledTask -TaskName 'ZeroDesk-AutoStart' -ErrorAction SilentlyContinue | Unregister-ScheduledTask -Confirm:$false -ErrorAction SilentlyContinue
$action = New-ScheduledTaskAction -Execute $exe -Argument '-tray' -WorkingDirectory $root
$trigger = New-ScheduledTaskTrigger -AtLogOn
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName 'ZeroDesk-AutoStart' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null

Write-Host "[3/3] 立即启动后台托盘服务并核验运行状态..."
Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 300
Start-ScheduledTask -TaskName 'ZeroDesk-AutoStart'

$alive = $null
for ($i = 0; $i -lt 6; $i++) {
    Start-Sleep -Milliseconds 500
    $alive = Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($alive) { break }
}

$pin = "请查看 data\config.json"
if (Test-Path -LiteralPath $cfgPath) {
    try {
        $cfg2 = Get-Content -LiteralPath $cfgPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($cfg2.pin_code) { $pin = $cfg2.pin_code }
        if ($cfg2.http_port) { $httpPort = [int]$cfg2.http_port }
    } catch {}
}

$lanIP = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
    Where-Object {
        $_.IPAddress -notlike '127.*' -and
        $_.IPAddress -notlike '169.254.*' -and
        $_.InterfaceAlias -notmatch 'VMware|Virtual|vEthernet|Hyper-V|WSL|Docker' -and
        ($_.IPAddress -match '^(192\.168\.|10\.|172\.(1[6-9]|2[0-9]|3[0-1])\.)')
    } | Select-Object -First 1 -ExpandProperty IPAddress)
if (-not $lanIP) { $lanIP = "127.0.0.1" }

Write-Host ""
Write-Host "============================================================" -ForegroundColor Cyan
if ($alive) {
    Write-Host ("  [成功] ZeroDesk 后台托盘服务已运行 (PID: {0})" -f $alive.Id) -ForegroundColor Green
} else {
    Write-Host "  [警告] 未检测到 zerodesk-host 进程存活，请检查 logs\zerodesk-host.log" -ForegroundColor Yellow
}
Write-Host ("  本机局域网访问网址 : http://{0}:{1}" -f $lanIP, $httpPort) -ForegroundColor White
Write-Host ("  6 位配对 PIN 码    : {0}" -f $pin) -ForegroundColor Yellow
Write-Host "  开机自启任务       : ZeroDesk-AutoStart (已启用，支持 UAC 穿透)"
Write-Host "============================================================" -ForegroundColor Cyan
Write-Host ""
Read-Host "按回车键关闭窗口"
