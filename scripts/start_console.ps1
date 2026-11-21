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

Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Set-Location -LiteralPath $root
& $exe
Read-Host "按回车键关闭窗口"
