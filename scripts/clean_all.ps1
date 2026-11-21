$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "正在请求管理员权限以清理系统级防火墙与任务规则..."
    Start-Process -FilePath "powershell.exe" -ArgumentList "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`"" -Verb RunAs
    exit 0
}

$root = Split-Path -Parent $PSScriptRoot
Write-Host "============================================================"
Write-Host "  ZeroDesk 一键彻底清理工具"
Write-Host "============================================================"

Write-Host "[1/4] 强杀 zerodesk-host.exe 进程..."
Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

Write-Host "[2/4] 精准删除名称匹配 ZeroDesk* 的 Windows 防火墙规则..."
Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'ZeroDesk*' -or $_.Name -like 'ZeroDesk*' } | Remove-NetFirewallRule -ErrorAction SilentlyContinue

Write-Host "[3/4] 清理任何 ZeroDesk* 计划任务与系统服务..."
Get-ScheduledTask -ErrorAction SilentlyContinue | Where-Object { $_.TaskName -like 'ZeroDesk*' } | Unregister-ScheduledTask -Confirm:$false -ErrorAction SilentlyContinue
Get-Service -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'ZeroDesk*' -or $_.DisplayName -like 'ZeroDesk*' } | ForEach-Object {
    Stop-Service $_.Name -Force -ErrorAction SilentlyContinue
    cmd.exe /c "sc.exe delete `"$($_.Name)`" >nul 2>&1"
}

Write-Host "[4/4] 解除只读属性并清空 .build_cache 与 .pkg_cache 缓存..."
foreach ($cacheName in @(".build_cache", ".pkg_cache")) {
    $cachePath = Join-Path $root $cacheName
    if (Test-Path -LiteralPath $cachePath) {
        cmd.exe /c "attrib -r -s -h /s /d `"$cachePath\*.*`" >nul 2>&1"
        cmd.exe /c "attrib -r -s -h `"$cachePath`" >nul 2>&1"
        Remove-Item -LiteralPath $cachePath -Recurse -Force -ErrorAction SilentlyContinue
        New-Item -ItemType Directory -Force -Path $cachePath | Out-Null
    }
}

Write-Host ""
Write-Host "============================================================" -ForegroundColor Green
Write-Host "系统级规则已全部清除，现在直接把桌面上的 ZeroDesk 文件夹拖进回收站即可 100% 无痕卸载" -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Green
Add-Type -AssemblyName PresentationFramework
[System.Windows.MessageBox]::Show("系统级规则已全部清除，现在直接把桌面上的 ZeroDesk 文件夹拖进回收站即可 100% 无痕卸载", "ZeroDesk 清理完成", "OK", "Information") | Out-Null
Read-Host "按回车键关闭窗口"
