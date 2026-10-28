# Windows proof rig, take 3: the sleeper and the watcher are created through WMI so they live outside the SSH
# session's job (sshd ends its children with the session; Claude Code's own hooks do not run under one).
$ErrorActionPreference = 'Continue'
$B = 'C:\src\brigade-dev.exe'; $root = 'C:\src\proof'
New-Item -ItemType Directory -Force -Path $root | Out-Null
Set-Location C:\src\win133
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\brigade\sessions","$env:LOCALAPPDATA\brigade\watchers" -ErrorAction SilentlyContinue
function Create-Detached([string]$cmdline, [string[]]$envs) {
  $startup = ([wmiclass]'Win32_ProcessStartup').CreateInstance()
  $startup.ShowWindow = 0
  if ($envs) { $startup.EnvironmentVariables = $envs }
  $r = ([wmiclass]'Win32_Process').Create($cmdline, 'C:\src\win133', $startup)
  if ($r.ReturnValue -ne 0) { throw "Win32_Process.Create returned $($r.ReturnValue)" }
  return $r.ProcessId
}
$sleeper = Create-Detached 'powershell.exe -NoProfile -WindowStyle Hidden -Command "Start-Sleep 7200"' $null
"sleeper pid: $sleeper"
$native = [guid]::NewGuid().ToString()
Set-Content -Path $root\start.json -Value "{`"session_id`":`"$native`",`"cwd`":`"C:\\src\\win133`",`"hook_event_name`":`"SessionStart`",`"source`":`"startup`"}" -Encoding ASCII
$sessionEnv = @{ CLAUDE_PID = "$sleeper"; CLAUDE_CODE_SESSION_ID = $native; CLAUDECODE = '1'; CLAUDE_CODE_ENTRYPOINT = 'cli'; CLAUDE_PLUGIN_ROOT = 'C:\src\brigade\plugin'; CLAUDE_PLUGIN_OPTION_TEAM_INBOUND = 'accept' }
foreach ($k in $sessionEnv.Keys) { [Environment]::SetEnvironmentVariable($k, $sessionEnv[$k]) }
"== hook session-start"
cmd /c "$B hook session-start < $root\start.json 2> $root\start.err"
Get-Content $root\start.err | Select-Object -First 3
$who = (cmd /c "$B whoami --json 2>&1" | ConvertFrom-Json).result
$session = $who.session_id
"session: $session"
foreach ($k in $sessionEnv.Keys) { [Environment]::SetEnvironmentVariable($k, $null) }
$profile = ((cmd /c "$B team status 2>&1" | ConvertFrom-Json).result).name
"== the sink watcher, detached through WMI"
$sink = "$root\sink.ndjson"
$base = [Environment]::GetEnvironmentVariables('Process').GetEnumerator() | Where-Object { $_.Key -in 'SystemRoot','windir','ComSpec','PATHEXT','TEMP','TMP','USERPROFILE','APPDATA','LOCALAPPDATA','SystemDrive','HOMEDRIVE','HOMEPATH','Path' } | ForEach-Object { "$($_.Key)=$($_.Value)" }
$wenv = @("BRIGADE_CLAUDE_PID=$sleeper", "BRIGADE_PROFILE=$profile", "BRIGADE_CONFIG_DIR=$env:APPDATA\brigade", "BRIGADE_STATE_DIR=$env:LOCALAPPDATA\brigade", 'BRIGADE_ADAPTER_COMMAND=[]', 'BRIGADE_TEAM_INBOUND=accept', 'BRIGADE_LOG_LEVEL=debug', 'CLAUDE_CODE_MESSAGING_TOKEN=sentinel-token-not-real') + $base
$watcher = Create-Detached "`"$B`" watch --sink `"$sink`" --log-level debug" $wenv
"watcher pid: $watcher"
$pidfile = "$env:LOCALAPPDATA\brigade\watchers\$sleeper.json"; $wlog = "$env:LOCALAPPDATA\brigade\logs\watcher-$sleeper.log"
$deadline = (Get-Date).AddSeconds(40)
while ((Get-Date) -lt $deadline) { if ((Test-Path $wlog) -and (Select-String -Path $wlog -Pattern '"msg":"watch ready"' -Quiet)) { break }; Start-Sleep -Milliseconds 300 }
"pidfile: $(Test-Path $pidfile)  ready: $((Test-Path $wlog) -and (Select-String -Path $wlog -Pattern '"msg":"watch ready"' -Quiet))  alive: $([bool](Get-Process -Id $watcher -ErrorAction SilentlyContinue))"
@{ sleeper = $sleeper; watcher = $watcher; session = $session; native = $native; profile = $profile } | ConvertTo-Json -Compress | Set-Content $root\ids.json
Get-Content $root\ids.json
