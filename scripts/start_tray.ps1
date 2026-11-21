$root = Split-Path -Parent $PSScriptRoot
$exe = Join-Path $root "zerodesk-host.exe"
if (-not (Test-Path -LiteralPath $exe)) {
    Add-Type -AssemblyName PresentationFramework
    [System.Windows.MessageBox]::Show("未找到 zerodesk-host.exe，请先完成编译！", "ZeroDesk 启动失败", "OK", "Error") | Out-Null
    exit 1
}

Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 200

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if ($isAdmin) {
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
    Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'ZeroDesk-LAN*' } | Remove-NetFirewallRule -ErrorAction SilentlyContinue
    $lanAddrs = @('192.168.0.0/16', '10.0.0.0/8', '172.16.0.0/12', 'LocalSubnet')
    New-NetFirewallRule -DisplayName 'ZeroDesk-LAN-TCP' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $httpPort -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
    New-NetFirewallRule -DisplayName 'ZeroDesk-LAN-UDP' -Direction Inbound -Action Allow -Protocol UDP -LocalPort $discPort -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
    New-NetFirewallRule -DisplayName 'ZeroDesk-LAN' -Direction Inbound -Action Allow -Protocol UDP -Profile Any -RemoteAddress $lanAddrs -Program $exe | Out-Null
}

Start-Process -FilePath $exe -ArgumentList '-tray' -WorkingDirectory $root -WindowStyle Hidden
