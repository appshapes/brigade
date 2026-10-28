$B = 'C:\src\brigade-dev.exe'; $root = 'C:\src\proof'
Set-Location C:\src\win133
$ids = Get-Content $root\ids2.json | ConvertFrom-Json
function Create-Detached([string]$cmdline, [string[]]$envs) {
  $startup = ([wmiclass]'Win32_ProcessStartup').CreateInstance(); $startup.ShowWindow = 0
  if ($envs) { $startup.EnvironmentVariables = $envs }
  $r = ([wmiclass]'Win32_Process').Create($cmdline, 'C:\src\win133', $startup)
  if ($r.ReturnValue -ne 0) { throw "Win32_Process.Create returned $($r.ReturnValue)" }
  return $r.ProcessId
}
$base = [Environment]::GetEnvironmentVariables('Process').GetEnumerator() | Where-Object { $_.Key -in 'SystemRoot','windir','ComSpec','PATHEXT','TEMP','TMP','USERPROFILE','APPDATA','LOCALAPPDATA','SystemDrive','HOMEDRIVE','HOMEPATH','Path' } | ForEach-Object { "$($_.Key)=$($_.Value)" }
$senv = @("CLAUDE_PID=$($ids.sleeper)", "CLAUDE_CODE_SESSION_ID=$($ids.native)", 'CLAUDECODE=1', 'CLAUDE_CODE_ENTRYPOINT=cli', 'CLAUDE_PLUGIN_ROOT=C:\src\brigade\plugin', 'CLAUDE_CODE_MESSAGING_SOCKET=\\.\pipe\LOCAL\cc-msg-brigade133', 'CLAUDE_CODE_MESSAGING_TOKEN=tok-brigade133-not-real') + $base
"watcher $($ids.watcher) alive before: $([bool](Get-Process -Id $ids.watcher -ErrorAction SilentlyContinue))"
$t0 = Get-Date
$hook = Create-Detached "cmd.exe /c `"`"$B`" hook session-end < $root\end2.json > $root\end2.out 2> $root\end2.err`"" $senv
$deadline = (Get-Date).AddSeconds(20); while ((Get-Date) -lt $deadline -and (Get-Process -Id $hook -ErrorAction SilentlyContinue)) { Start-Sleep -Milliseconds 100 }
"hook session-end took $([int]((Get-Date) - $t0).TotalMilliseconds) ms"
Get-Content $root\end2.err -ErrorAction SilentlyContinue | Select-Object -First 4
$deadline = (Get-Date).AddSeconds(10); while ((Get-Date) -lt $deadline -and (Get-Process -Id $ids.watcher -ErrorAction SilentlyContinue)) { Start-Sleep -Milliseconds 100 }
"watcher alive after: $([bool](Get-Process -Id $ids.watcher -ErrorAction SilentlyContinue))  ($([int]((Get-Date) - $t0).TotalMilliseconds) ms after the hook started)"
"pidfile present: $(Test-Path "$env:LOCALAPPDATA\brigade\watchers\$($ids.sleeper).json")"
"== watcher log tail:"; Get-Content "$env:LOCALAPPDATA\brigade\logs\watcher-$($ids.sleeper).log" | Select-Object -Last 5
Stop-Process -Id $ids.sleeper,$ids.server -Force -ErrorAction SilentlyContinue
