param([string]$Name = 'cc-msg-brigade133', [string]$Log = 'C:\src\proof\pipe.log')
# A stand-in for Claude Code's inbox: one named pipe in the LOCAL namespace, one client at a time, every line
# it receives appended to the log with a timestamp. The watcher connects, writes two lines and closes.
while ($true) {
  $server = New-Object System.IO.Pipes.NamedPipeServerStream("LOCAL\$Name", [System.IO.Pipes.PipeDirection]::In, 1, [System.IO.Pipes.PipeTransmissionMode]::Byte)
  $server.WaitForConnection()
  $reader = New-Object System.IO.StreamReader($server)
  while (($line = $reader.ReadLine()) -ne $null) { Add-Content -Path $Log -Value ("[{0}] {1}" -f (Get-Date).ToUniversalTime().ToString('o'), $line) }
  $server.Dispose()
}
