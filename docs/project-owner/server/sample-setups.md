# Sample setups

Two setups that run today, with their names and paths made into examples. Both keep the server on loopback and
put [Caddy](https://caddyserver.com) in front of it on the same machine. Caddy gets a Let's Encrypt certificate
for the server's name and renews it by itself. So the team file names `https://brigade.example.com` and no
fingerprint, and the gateways can be opened.

## What both share

```sh
brigade-server --data <dir> --listen 127.0.0.1:8750 --create-token-file <file> --gateway-config <file>
```

- Plain HTTP on loopback needs no TLS option, and nothing between Caddy and the server reads a message.
- Both servers are on the internet, so creating a team needs the token
  ([Who may create a team](run-the-server.md#who-may-create-a-team)).
- `--gateway-config` is there once the gateways are opened ([Open the gateways](gateways.md)).
- A copy is taken each night with the server stopped, leaves the machine, and something outside the machine says
  when it did not ([Keep the server](keep-the-server.md)).

## A machine of your own, behind a router

| | |
| --- | --- |
| Machine | a Linux machine that stays on, at home or in an office. No cloud account. |
| Runs as | your own user: two systemd user units. `loginctl enable-linger <user>` starts them with the machine. |
| Files | one directory, `~/brigade-server`: `bin/`, `data/`, `Caddyfile`, `gateways.json`, `create.token`, and `secrets/` (mode 0700) for the gateways' secret files |
| Network | the name's address record is the router's public address. The router forwards TCP 443 to this machine's 8743 and nothing else: the certificate arrives over that port. |
| Nightly copy | a timer runs a script: stop the server, archive `data` and the configuration, start it, keep the newest 14 |
| Second machine | pulls the copies each night with a key that may only read that folder. A pull that fails posts to Slack. |

`~/.config/systemd/user/brigade-server.service`. Caddy's unit has the same shape, with
`caddy run --config <the Caddyfile> --adapter caddyfile` as its `ExecStart`:

```ini
[Unit]
Description=Brigade server
After=network-online.target

[Service]
ExecStart=%h/brigade-server/bin/brigade-server --data %h/brigade-server/data --listen 127.0.0.1:8750 --create-token-file %h/brigade-server/create.token --gateway-config %h/brigade-server/gateways.json
Restart=on-failure
TimeoutStopSec=30

[Install]
WantedBy=default.target
```

`~/brigade-server/Caddyfile`. Caddy listens on 8743, so it needs no root:

```
{
	https_port 8743
	http_port 8080
	auto_https disable_redirects
	admin off
	storage file_system /home/<user>/brigade-server/caddy-data
}

brigade.example.com {
	reverse_proxy 127.0.0.1:8750
}
```

## A cloud instance

This one is an AWS EC2 instance. It was made with the cloud's command line from a written description, not from
infrastructure code: what exists, each value and why, the order, and the checks. The table is that description
in outline.

| | |
| --- | --- |
| Machine | one small instance with 2 GiB of memory, Ubuntu 24.04, in a network that reaches nothing else of the account |
| Network | a fixed public address, and the name's address record on it. Inbound TCP 443 only: Caddy asks for its certificate over that port, and port 80 stays shut. |
| Shell | no SSH and no key pair: Session Manager |
| Data | an encrypted volume of its own at `/srv/brigade`, kept when the instance is replaced: the server's data in `data/`, Caddy's certificate in `caddy/` |
| Runs as | two system users, `brigade` and `caddy`, each with a system unit |
| Secret files | SecureString parameters under one path of Parameter Store. A step that runs as root before each start writes each one to a file in `/run`, which is memory. The instance may read that path and no other. |
| Nightly copy | a timer runs a script: stop the server, archive `data`, start it, put the archive in an S3 bucket. The instance may put there, and may not read, list or delete. Copies end after 35 days. |
| Watch | a scheduled function outside the instance asks `POST /v1/info`, and reads the newest copy's age and the certificate's end. It posts to Slack when one of the three changes. |

`/etc/systemd/system/brigade-server.service`. Caddy's unit has the same last four lines of `[Service]`:

```ini
[Unit]
Description=Brigade server
After=network-online.target brigade-server-secrets.service
Wants=network-online.target brigade-server-secrets.service
RequiresMountsFor=/srv/brigade
AssertPathIsMountPoint=/srv/brigade

[Service]
User=brigade
ExecStart=/usr/local/bin/brigade-server --data /srv/brigade/data --listen 127.0.0.1:8750 --create-token-file /run/brigade-server/secrets/create-token
Restart=on-failure
TimeoutStopSec=30
NoNewPrivileges=yes
ProtectSystem=full
ProtectHome=yes
IPAddressDeny=169.254.169.254

[Install]
WantedBy=multi-user.target
```

- `AssertPathIsMountPoint`: with the volume missing nothing starts, so no server ever starts on an empty
  directory and becomes a new backend.
- `IPAddressDeny`: neither the server nor Caddy reaches the instance's metadata address, so neither can take
  the instance's cloud credentials. Only root's own steps use them: the secret files and the copy.
- `brigade-server-secrets.service` is a `Type=oneshot` unit that runs the step for the secret files.
- On the instance `ExecStart` is a short script with this line in it: it adds `--gateway-config` when that file
  is there, and refuses to start when the creation token's file is not.

`/etc/caddy/Caddyfile`. Caddy's unit has `AmbientCapabilities=CAP_NET_BIND_SERVICE` for port 443:

```
{
	admin off
	storage file_system /srv/brigade/caddy
	auto_https disable_redirects
}

brigade.example.com {
	tls {
		issuer acme {
			disable_http_challenge
		}
	}
	reverse_proxy 127.0.0.1:8750
}
```

## Which one

| | A machine of your own | A cloud instance |
| --- | --- | --- |
| Costs | nothing beyond the machine | a small instance, a volume and a bucket each month |
| Depends on | the place's power, its line and its router | the cloud account, and who administers it |
| After the machine is lost | a new machine, the copy from the second one | a new instance on the same volume, or the copy from the bucket |
