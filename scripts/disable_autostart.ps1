$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "正在请求管理员权限以移除开机自启计划任务..."
    Start-Process -FilePath "powershell.exe" -ArgumentList "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`"" -Verb RunAs
    exit 0
}

Write-Host "正在删除计划任务 ZeroDesk-AutoStart..."
Get-ScheduledTask -TaskName 'ZeroDesk-AutoStart' -ErrorAction SilentlyContinue | Unregister-ScheduledTask -Confirm:$false -ErrorAction SilentlyContinue
cmd.exe /c 'schtasks /Delete /TN "ZeroDesk-AutoStart" /F >nul 2>&1'
Write-Host "[完成] 已关闭 ZeroDesk 开机自启。" -ForegroundColor Green
Read-Host "按回车键关闭窗口"
