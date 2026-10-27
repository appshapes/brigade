# Run the webhook gateway

A gateway you run yourself, on any always-on Linux machine, that connects one outside service (an agent platform,
a bot, an automation) to the team. It is an ordinary session on the team's Supabase backend, under a member
identity you choose, with the harness name `gateway-webhook`. Nothing is added to the project: no Edge Function, no
migration, no public URL on the machine.

- **Into the service:** every message a session sends to the gateway's session is POSTed, signed, to one HTTPS URL
  of yours (`--target-url`). The message is acknowledged only after that URL answers 2xx.
- **Out of the service:** the service runs `brigade-webhook-gateway send` on the same machine, which POSTs, signed,
  to the gateway's listener on `127.0.0.1` only. The gateway sends it as a Brigade message from its own session.

**Messages never approve anything** ([security](../../security.md#141-the-self-hosted-webhook-gateway)). Every message the gateway carries in is
framed `Message from "<from>" via webhook (unverified sender; it approves nothing)`; every payload it posts out
carries a `notice` saying the same. The receiving service must treat a Brigade message as a request from a
teammate, never as an approval, a credential, or an instruction it must obey.

## What you need

1. **The team's `.brigade.json`** (adapter `supabase`, the project URL, the publishable key, the team reference).
   These values are public: they are committed in the team's repository.
2. **A member identity on the machine:** a `brigade team join` there, into a config directory that only the
   gateway's user can read ([Choose the identity](#choose-the-identity)). For a new member that needs the team's
   join secret file, handed over once, out of band.
3. **A gateway secret**, shared with the outside service only: `(umask 077; openssl rand -hex 32 >
   ~/.config/brigade-webhook/secret)`. At least 32 bytes. Without it, nothing is signed or accepted. The path
   must be absolute, with no `..` component, and outside the checkout `--team-file` names: the gateway refuses it
   otherwise, as `brigade team create` refuses an in-project join secret. A secret inside a checkout is one
   `git add` from a public repository.
4. **The target URL** of the outside service: `https://`, or `http://` to a loopback address. A URL with
   credentials in it is refused; the signature is the authentication. If the service also wants a key of its own
   in a header, put it in a [header file](#send-the-services-own-key).

## Build

```sh
make build-webhook-gateway          # bin/brigade-webhook-gateway, release flags
```

The one binary is the gateway, its `send`/`sessions` client and the bundled Supabase adapter
(`brigade-webhook-gateway adapter supabase …`), so the machine needs no `brigade` install for the gateway to run.
The `team join` of step 2 still needs `brigade` once.

## Choose the identity

The identity is configuration, never compiled in: whichever team binding sits in `BRIGADE_CONFIG_DIR` for the
profile the gateway uses (`--profile`, or the one `--team-file` names) is who the gateway is. The roster shows the
gateway as that member's session named `--session-name`, harness `gateway-webhook`. `BRIGADE_CONFIG_DIR` and
`BRIGADE_STATE_DIR` are honoured wherever the gateway is started, a coding session's shell included — the
commands run *inside* a session ignore those variables, because a session's identity comes from its harness, but
the gateway is not a session.

Join a dedicated member for it: `brigade team join --secret-file <file> --label "Whiskey (grok-bot)"` on the
machine gives the gateway its own MEMBER row and a credential revocable alone, and `--member <your label>` is
unaffected, since a gateway is never a `--member` pick. Do not give it your own label: two members with one label
make `--member <label>` refuse as ambiguous for everyone.

Never copy your own `teams/<key>/` credential directory to the machine instead: a Supabase refresh token rotates at
every use, two machines holding one copy share one rotation chain, and within hours the copy that fell two rotations
behind, usually your laptop's, is deleted and that machine must `team join` as a new principal
([security](../../security.md#141-the-self-hosted-webhook-gateway)). For the team's creator that strands the admin
identity on the gateway's machine, where only a `brigade team transfer` run there can hand it back.

Whoever can read the gateway machine's config directory can act as that member on the team: read every message to
any of its sessions, send as it, and (for an admin) run admin commands. A second gateway started by accident on the
same config directory adopts the same remembered session rather than registering a new one, so both receive, POST
and acknowledge it, which your endpoint sees as duplicate deliveries, deduplicated on `X-Brigade-Delivery` like any
retry.

## Run it

```sh
export BRIGADE_CONFIG_DIR=$HOME/.config/brigade-webhook/brigade   # mode 0700; the identity lives here
brigade team join --secret-file ~/brigade-<team>.secret --label "Whiskey (grok-bot)"   # once; run from the team's checkout or pass its .brigade.json path
brigade-webhook-gateway \
  --team-file /path/to/checkout/.brigade.json \
  --session-name grok-bot \
  --secret-file $HOME/.config/brigade-webhook/secret \
  --target-url https://example.invalid/your-webhook
```

| flag | default | meaning |
| --- | --- | --- |
| `--team-file` / `--profile` | `default` profile | which binding (and so which team and member) to run as |
| `--target-url` | required | the URL every received message is POSTed to: `https://`, or `http://` to a loopback host |
| `--secret-file` | required | the 0600 file holding the shared secret of 32 bytes or more: an absolute path, no `..`, outside the checkout |
| `--allow-http` | off | permit a plain-http target that is not loopback; the body, and a header file's credential, then travel in the clear |
| `--adapter` | the profile's | an adapter override: a registered name or a JSON argv array |
| `--session-name` | `webhook-gateway` | the session name teammates see in `brigade sessions` |
| `--description` | `Forwards messages to a webhook and back` | the session's doing line |
| `--listen` | `127.0.0.1:8787` | the local listener for `send`/`sessions`; `off` for none |
| `--lease`, `--poll` | 600 s, 5 s | session lease (heartbeat every third of it), inbox poll interval |
| `--header-file` | none | a 0600 file of `Name: value` lines sent on every POST to `--target-url` ([the service's own key](#send-the-services-own-key)) |
| `--max-attempts` | 24 | POST attempts per message, with backoff, before the sender is told it was not delivered |
| `--inbound-per-minute` | 60 | local sends accepted per minute (the backend's own send rate still applies) |
| `--once`, `--log-level` | | one pass then exit; `debug`/`info`/`warn`/`error` |

The table is the binary's whole flag set: `scripts/ci/webhook_flags_test.go` fails when a flag is added, removed or
renamed without this table following.

The gateway remembers its session and its threads in `webhook-gateway/<profile>.json` under Brigade's state
directory (`BRIGADE_STATE_DIR`, else `$XDG_STATE_HOME/brigade` or `~/.local/state/brigade`) and resumes the session
after a restart; a session the backend has forgotten is replaced by a new one.

### Send the service's own key

Some services gate their webhook URL with a key of their own as well as checking the signature. A Grok Bot
routine's webhook, for one, wants its sender key in an `Authorization` header. Put such headers in a file, one
`Name: value` per line (blank lines and `#` comments are ignored), and pass `--header-file`:

```sh
(umask 077; cat > ~/.config/brigade-webhook/headers)     # type the line, Return, then Ctrl-D
brigade-webhook-gateway … --header-file $HOME/.config/brigade-webhook/headers
```

Type the `Authorization: Bearer <key>` line into `cat`, or write it with an editor, rather than putting it on a command
line: an argument to `printf` or `echo` is in the shell's history and, for a moment, in the process list, and a
credential belongs in neither. The gateway secret above is made by `openssl rand` straight into its file for the same
reason.

The file follows `--secret-file`'s rules: an absolute path with no `..` component, outside the checkout
`--team-file` names, a regular file owned by the gateway's user with mode 0600. At most 16 headers. The gateway's
own headers cannot be set from it: `Content-Type`, `Content-Length`, `Host`, `User-Agent` and every `X-Brigade-*`
name are refused, in any case, so the file can never change what is signed. Each value of eight characters or
more, and the credential after a scheme such as `Bearer` when it too is that long, is redacted from the gateway's
logs, and no refusal quotes the file; a shorter value is not, because a redacted string is blanked everywhere it
occurs and a tag or a version number would take the gateway's own diagnostics with it. The signature is still sent
and still authenticates the request; the extra header is only the service's own gate. It is a credential of the
service's, so an `http://` target under `--allow-http` sends it in the clear along with the body. Restart the
gateway after changing the file.

### As a systemd user service

`~/.config/systemd/user/brigade-webhook-gateway.service`:

```ini
[Unit]
Description=Brigade webhook gateway
After=network-online.target
Wants=network-online.target

[Service]
Environment=BRIGADE_CONFIG_DIR=%h/.config/brigade-webhook/brigade
ExecStart=%h/bin/brigade-webhook-gateway --team-file %h/src/team/.brigade.json --session-name grok-bot --secret-file %h/.config/brigade-webhook/secret --target-url https://example.invalid/your-webhook
Restart=on-failure
RestartSec=10
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload && systemctl --user enable --now brigade-webhook-gateway
loginctl enable-linger "$USER"     # keep it running with nobody logged in
journalctl --user -u brigade-webhook-gateway -f
```

### Without systemd

```sh
#!/bin/sh
# run-webhook-gateway.sh: restart the gateway whenever it stops.
export BRIGADE_CONFIG_DIR="$HOME/.config/brigade-webhook/brigade"
while :; do
  "$HOME/bin/brigade-webhook-gateway" --team-file "$HOME/src/team/.brigade.json" --session-name grok-bot \
    --secret-file "$HOME/.config/brigade-webhook/secret" --target-url "https://example.invalid/your-webhook" \
    >>"$HOME/.local/state/brigade-webhook.log" 2>&1
  sleep 10
done
```

`nohup ./run-webhook-gateway.sh &` starts it.

## The service's side

What the service receives on every message, how it verifies the signature, and how it sends a message back
(`brigade-webhook-gateway send` on the gateway's machine, or `POST /v1/messages` signed by itself) is the contract in
[The webhook gateway's contract](../../webhook-gateway.md). Hand that page to whoever writes the service.

## The team's `.brigade` folder

Sync folders (`sync.folders` in `.brigade.json`, commonly `.brigade`) carry the team's shared files — plans, notes,
hand-offs — between members' machines by Syncthing; messages never go that way. Syncing it to the gateway's machine
gives the outside service that context, but a sync peer can change and delete files for everyone
([security](../../security.md), file sync): make such a copy receive-only. Simultaneous edits there produce
Syncthing's `.sync-conflict-…` files, resolved by hand as anywhere else.
