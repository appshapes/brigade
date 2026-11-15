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

Going back to an earlier release is not always possible, and the release's notes say when. From 0.8.0: a server
before 0.8.0 does not start on a data directory on which a team's owner has recorded that the team moved away
([Move a team to another server](move-to-another-server.md)), until that record is cleared and the server has
stopped once since. A 0.7.0 ends with status 1, and what it writes first is one of two things. Where a snapshot
holds the record, as one does once the 0.8.0 has stopped, started again or reached its next snapshot since the
record, it writes `the snapshot <file> is passed over: it is of format 4, and this build reads formats 1 to 3`,
once for each such snapshot it meets before one it can read, and then `the log could not be read from the
snapshot's position, <n>: not an entry: unknown variant `team.move_recorded`, expected one of …`. Where the 0.8.0
was killed after the record and before its next snapshot, no snapshot holds the record yet, and it writes the
second line alone. Neither line names the version to run, because 0.7.0 was released before there was one: it is
0.8.0 or later. On a directory where no move was recorded, 0.7.0 and 0.8.0 start on each other's.

## Move it

**To another machine, at the same URL.** Stop the server, copy the data directory to the new machine, and start
the server there with the same options. Members reach it by the URL they know, and nothing else changes.

**To another URL.** A changed URL is a changed backend to a member. Do not start the copy at the new URL: a
server that holds the teams already is no place to move them to
([why](move-to-another-server.md#the-server-the-team-goes-to-must-not-be-a-copy)). From server 0.8.0 and plugin
0.57.0, start a fresh server at the new URL and keep the old one running: each Team Admin moves their team with
one command, `brigade team move --to <url> --secret-file <path>`, and the members follow with no command of
theirs ([Move a team to another server](move-to-another-server.md)). Stop the old server when every team has
moved and its members have followed. With an older server or plugin, each member joins the new URL with the
secret file, as before.

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

## Watch its certificate

A certificate from a public authority ends, whether a proxy in front of the server holds it or you gave it with
`--tls-cert`. Caddy renews its own when a third of its life is left; one you gave the server is yours to renew,
with a restart after. A renewal that fails or is forgotten says nothing: on the day the certificate ends, every
member's session stops connecting. So the second machine, the one that tells you of a failed copy, asks the name
on a timer and posts to the same Slack webhook:

- when the name stops answering `POST /v1/info`, and when it answers again, each once;
- once a day while the certificate has less than a sixth of its life left. Caddy's renewal has failed for days
  by then, and days are left to act.

It reads the certificate's two dates at the name and the port a member reaches, and compares what is left with
the whole:

```sh
echo | openssl s_client -connect <host>:<port> -servername <host> 2>/dev/null |
	openssl x509 -noout -startdate -enddate -dateopt iso_8601
```

Four things keep it honest. It asks more than once before it calls the name down, because a nightly copy stops
the server for some seconds. It keeps a line it could not post and posts it at its next run: a second machine
that shares the server's connection hears of that connection's outage only when it is over. It says so when the
name answers and the dates cannot be read, because a watch that sees nothing and reports nothing looks like good
news. And you run it once with the threshold above what is left, so that you have seen its line arrive.

A certificate made by `--tls-self-signed` is pinned, and its dates are not consulted: there is nothing to watch.

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

One instance. One member's installation is bounded ([Run the server](run-the-server.md#what-one-member-may-do-in-an-hour));
nothing bounds the teams on a server or the members a join secret admits.
[Change the server](../../developer/change/server.md#known-limits) lists the limits the reviews of the code left
open.
