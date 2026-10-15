-- Who may write to a team's mail gateway (Trello card 55; docs/mail-gateway.md "Who may write"). A mail's From is
-- unverified text, and until now the gateway answered anyone who wrote to its published address with the team's
-- session list: who is on the team, what each session is doing and the id tails that address them. The senders a
-- team expects go in this column, as lowercase addresses (`qa@example.com`) and domains (`@example.com`); mail-in
-- reads it on every mail and drops a mail from anyone else before it looks anything up or answers. The list is kept
-- by `make gateway-senders`, never by the installer, so a re-run of the installer leaves it alone. Null or empty
-- keeps the gateway open to anyone, as every row was before this migration.
alter table brigade_gateway.gateways
  add column allowed_senders text[] check (coalesce(cardinality(allowed_senders), 0) <= 200);
