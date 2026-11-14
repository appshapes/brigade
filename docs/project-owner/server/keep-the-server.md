# Keep the server

## Back it up

Everything the server knows is in its data directory: the log, the snapshots, the key the log is sealed under,
with `--tls-self-signed` the certificate every team file pins, and with the gateways on, their own records in
`gateway/`.

```sh
sudo systemctl stop brigade-server
sudo tar -C /var/lib -czf brigade-server-$(date +%F).tar.gz brigade-server
sudo systemctl start brigade-server
```

Copy the directory with the server stopped: a copy taken while it runs is not one the server is known to start
from. Keep the copy where only the server's operator can read it. It holds every message, each member's
credential in hashed form and the certificate's key.

## Hear when a copy fails

A copy nobody checks fails unseen. Take the copies on a timer, pull them to a second machine, and let that
machine tell you when a pull fails. The news leaves from the second machine and never through the server: when
the server's machine is down, the copy fails and the server cannot carry it.

1. The pull is a unit on the second machine that a timer runs. It ends in failure when the newest copy it holds
   is older than the copies' period allows, 26 hours for a nightly one.
2. A drop-in names the unit that runs when the pull fails:

   ```ini
   # ~/.config/systemd/user/<the pull>.service.d/tell.conf
   [Unit]
   OnFailure=<the notice>.service
   ```

3. That unit posts one line to a Slack incoming webhook: that the copy failed, on which machine, and the last
   line the failed pull wrote. The webhook's address is in a file only you can read. It is a secret, so it
   reaches `curl` on standard input and never on a command line:

   ```sh
   printf 'url = "%s"\n' "$(head -n 1 "$address_file")" |
   	curl -fsS -m 20 -o /dev/null -X POST -H 'Content-Type: application/json' --data "$json" -K -
   ```

With no address in the file the unit says so in its journal and ends well, so it can be in place before the
webhook exists. What this does not cover: a second machine that is itself down tells nobody.

## What is lost with the disk

There is one instance and no standby, so the data directory and your copies of it are all there is.

**With no copy:** every team on the server, with its members, sessions and messages. A new server is then a new
backend: the Team Admin creates the team again and every member joins again.

**With a copy:** put it back as the data directory, with its owner and modes, and start the server with the same
options, as in [Move it](#move-it). The teams return as they were when the copy was taken. A member who had
joined by then is a member again under the credential their machine holds, and sessions register again as they
start. What happened after the copy is gone: the messages, a member who joined since, who joins again, and a
join secret rotated since: the earlier one joins again, so rotate it again.

**What a copy of the data directory does not hold.** These files live outside it. Keep a copy of each where you
choose, or place them again after a loss:

| File | Named by | After a loss |
| --- | --- | --- |
| The certificate and its key | `--tls-cert`, `--tls-key` | The same files, or a new certificate for the same name. A certificate made by `--tls-self-signed` is in the data directory and returns with the copy. |
| The creation token | `--create-token-file` | Any new value, given again to whoever creates teams. |
| The gateways' configuration | `--gateway-config` | Written again ([Open the gateways](gateways.md)). |
| Each team's join secret | `join_secret_file` | The file the Team Admin has. |
| Slack's bot token and signing secret | `bot_token_file`, `signing_secret_file` | Copied again from the Slack app's pages. |
| The mail provider's key | `api_key_file` | A key from the provider. |
| The mail webhook's secret | `webhook_secret_file` | From the provider. One that does not show a webhook's secret again needs a new webhook at the same address. |

So do the service's unit and the configuration of anything that stands in front of the server. A gateway's own
membership is in the data directory: with the copy back and these files in place, the gateways return as the
members they were.

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

## Watch it

One request needs no credential and says nothing about any team: `POST /v1/info` with the body `{}`. It is the
one for a monitor. An answer with status 200 says the server is up and answering, and names its version.

```sh
curl -fsS -X POST -H 'Content-Type: application/json' -d '{}' https://<host>/v1/info
```

## What it writes

The server writes lines to its standard error, which a service's journal keeps: its start and where it
listens, its snapshots, its stop and its gateways' own turns. From 0.4.1 it also writes one line when a member
creates a team, joins one or becomes its owner, and one for each request to a gateway's address:

```
member 5f0c… joined team ops (3b89…), labelled "dana@example.com"
the slack gateway took a request (200)
the slack gateway refused a request its Slack app did not sign (401)
```

```sh
journalctl -u brigade-server | grep ' joined team '
```

No line carries a credential, a join secret, a token or a message's text. A join's line carries the member's
label, which is often an email address, so keep the journal as private as the data directory.

## What it does not do yet

One instance; nothing bounds the teams on a server or the sessions a member registers.
[Change the server](../../developer/change/server.md#known-limits) lists the limits the reviews of the code left
open.
