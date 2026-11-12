# Keep the server

## Back it up

Everything the server knows is in its data directory: the log, the snapshots, the key the log is sealed under and,
with `--tls-self-signed`, the certificate every team file pins.

```sh
sudo systemctl stop brigade-server
sudo tar -C /var/lib -czf brigade-server-$(date +%F).tar.gz brigade-server
sudo systemctl start brigade-server
```

Copy the directory with the server stopped: a copy taken while it runs is not one the server is known to start
from. Keep the copy where only the server's operator can read it. It holds every message, each member's
credential in hashed form and the certificate's key.

## What is lost with the disk

Every team on the server, with its members, sessions and messages. There is one instance and no standby in 0.1.0.
After a loss, a new server is a new backend: the Team Admin creates the team again and every member joins again.

## Update it

```sh
sudo systemctl stop brigade-server
sudo install -m 0755 brigade-server_<version>_linux_amd64 /usr/local/bin/brigade-server
sudo systemctl start brigade-server
```

The data directory carries over. A release whose notes say that the log's format changed says there how to carry
a directory across; until the log's own format is frozen, that can mean starting a new log from the newest
snapshot.

## Move it

Stop the server, copy the data directory to the new machine, and start the server there with the same options. If
members reach it by the same URL, nothing else changes. A changed URL is a changed backend to a member: each one
joins again with the secret.

## Replace its certificate

With `--tls-self-signed` the certificate is made once and kept. To have a new one: stop the server, remove
`<data>/tls`, start it, and read the new fingerprint from the start line. Then put the new fingerprint in the
`certificate` member of each repository's `.brigade.json` and commit it. Until a member has pulled that change and
run `brigade team join`, which shows the old and the new fingerprint and needs no secret, their sessions connect
to nothing.

## What it does not do yet

One instance; no mail or Slack gateway; nothing bounds the teams on a server or the sessions a member registers.
[Change the server](../../developer/change/server.md#known-limits) lists the limits the reviews of the code left
open.
