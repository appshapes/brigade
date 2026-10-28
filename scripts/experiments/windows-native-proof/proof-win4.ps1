# Windows proof, the wake path: a session whose inbox is a named pipe (as Claude Code's is, E11), the real
# `hook session-start` spawning the real detached watcher, a pipe server standing in for the session, and then
# `hook session-end` stopping the watcher through the stop event. Everything long-lived is created through WMI.
$ErrorActionPreference = 'Continue'
$B = 'C:\src\brigade-dev.exe'; $root = 'C:\src\proof'
Set-Location C:\src\win133
Remove-Item $root\pipe.log -ErrorAction SilentlyContinue
function Create-Detached([string]$cmdline, [string[]]$envs) {
  $startup = ([wmiclass]'Win32_ProcessStartup').CreateInstance(); $startup.ShowWindow = 0
  if ($envs) { $startup.EnvironmentVariables = $envs }
  $r = ([wmiclass]'Win32_Process').Create($cmdline, 'C:\src\win133', $startup)
  if ($r.ReturnValue -ne 0) { throw "Win32_Process.Create returned $($r.ReturnValue)" }
  return $r.ProcessId
}
$base = [Environment]::GetEnvironmentVariables('Process').GetEnumerator() | Where-Object { $_.Key -in 'SystemRoot','windir','ComSpec','PATHEXT','TEMP','TMP','USERPROFILE','APPDATA','LOCALAPPDATA','SystemDrive','HOMEDRIVE','HOMEPATH','Path' } | ForEach-Object { "$($_.Key)=$($_.Value)" }
$pipe = '\\.\pipe\LOCAL\cc-msg-brigade133'
$server = Create-Detached 'powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File C:\src\pipe-server.ps1' $base
$sleeper = Create-Detached 'powershell.exe -NoProfile -WindowStyle Hidden -Command "Start-Sleep 7200"' $base
"pipe server pid: $server  sleeper pid: $sleeper"
Start-Sleep 2
$native = [guid]::NewGuid().ToString()
Set-Content -Path $root\start2.json -Value "{`"session_id`":`"$native`",`"cwd`":`"C:\\src\\win133`",`"hook_event_name`":`"SessionStart`",`"source`":`"startup`"}" -Encoding ASCII
Set-Content -Path $root\end2.json -Value "{`"session_id`":`"$native`",`"cwd`":`"C:\\src\\win133`",`"hook_event_name`":`"SessionEnd`"}" -Encoding ASCII
$senv = @("CLAUDE_PID=$sleeper", "CLAUDE_CODE_SESSION_ID=$native", 'CLAUDECODE=1', 'CLAUDE_CODE_ENTRYPOINT=cli', 'CLAUDE_PLUGIN_ROOT=C:\src\brigade\plugin', 'CLAUDE_PLUGIN_OPTION_TEAM_INBOUND=accept', "CLAUDE_CODE_MESSAGING_SOCKET=$pipe", 'CLAUDE_CODE_MESSAGING_TOKEN=tok-brigade133-not-real', 'CLAUDE_PLUGIN_OPTION_LOG_LEVEL=debug') + $base
Remove-Item $root\start2.out,$root\start2.err -ErrorAction SilentlyContinue
"== hook session-start (through WMI, so its watcher outlives this shell)"
$hook = Create-Detached "cmd.exe /c `"`"$B`" hook session-start < $root\start2.json > $root\start2.out 2> $root\start2.err`"" $senv
$deadline = (Get-Date).AddSeconds(30); while ((Get-Date) -lt $deadline -and (Get-Process -Id $hook -ErrorAction SilentlyContinue)) { Start-Sleep -Milliseconds 300 }
Get-Content $root\start2.out; Get-Content $root\start2.err | Select-Object -First 6
$pidfile = "$env:LOCALAPPDATA\brigade\watchers\$sleeper.json"; $wlog = "$env:LOCALAPPDATA\brigade\logs\watcher-$sleeper.log"
$deadline = (Get-Date).AddSeconds(40); while ((Get-Date) -lt $deadline) { if ((Test-Path $wlog) -and (Select-String -Path $wlog -Pattern '"msg":"watch ready"' -Quiet)) { break }; Start-Sleep -Milliseconds 300 }
"pidfile: $(Test-Path $pidfile)  ready: $((Test-Path $wlog) -and (Select-String -Path $wlog -Pattern '"msg":"watch ready"' -Quiet))"
$pf = Get-Content $pidfile -ErrorAction SilentlyContinue | ConvertFrom-Json
"watcher pid: $($pf.pid)  socket_path: $($pf.socket_path)  alive: $([bool](Get-Process -Id $pf.pid -ErrorAction SilentlyContinue))"
"session: $($pf.brigade_session_id)"
@{ sleeper = $sleeper; watcher = $pf.pid; server = $server; session = $pf.brigade_session_id; native = $native } | ConvertTo-Json -Compress | Set-Content $root\ids2.json
