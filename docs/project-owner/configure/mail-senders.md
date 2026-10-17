# Choose who may write

By default anyone who knows the address can write to the mail gateway, and a mail that names no session, or says
`sessions`, is answered with the list of sessions: who is on the team, what each session is doing and the characters
that address it. List the people you expect, and a mail from anyone else is dropped before anything is looked up or
answered:

```sh
read -rs SUPABASE_ACCESS_TOKEN && export SUPABASE_ACCESS_TOKEN
make gateway-senders project=<ref> team_file=<path>/.brigade.json add='qa@your-domain.com,@partner.example'
make gateway-senders project=<ref> team_file=<path>/.brigade.json remove='qa@your-domain.com'
make gateway-senders project=<ref> team_file=<path>/.brigade.json             # shows the list
make gateway-senders project=<ref> team_file=<path>/.brigade.json clear=1     # open to anyone again
```

- An entry is an address (`qa@your-domain.com`) or a domain with a leading `@` (`@partner.example`: every address
  on exactly that domain, not its subdomains; a bare `partner.example` is read the same way). The From address is
  matched case-insensitively.
- The list lives in the team's gateway row and `mail-in` reads it on every mail, so a change takes effect on the
  next mail: no redeploy, no provider key, only the access token. The installer never touches the list.
- A dropped mail gets no answer, which keeps the gateway from confirming its address to strangers and from bouncing
  to forged From addresses; the function logs the sender's domain.
- The list is a bar on strangers, not authentication: a From address is unverified text, and someone who knows a
  listed address can put it on a mail. Sessions see `(unverified)` beside every sender for that reason. The Slack
  gateway needs no list: Slack verifies every request and names the workspace member.
