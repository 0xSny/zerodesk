$procs = Get-Process -Name 'zerodesk-host' -ErrorAction SilentlyContinue
if ($procs) {
    $procs | Stop-Process -Force -ErrorAction SilentlyContinue
    Write-Host "[完成] zerodesk-host.exe 已停止。" -ForegroundColor Green
} else {
    Write-Host "[提示] 当前未检测到运行中的 zerodesk-host.exe 进程。"
}
