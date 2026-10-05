-- Several teams' mail gateways in one project (Trello card 49, docs/mail-gateway.md "Several teams"). The settings
-- that differ per team move from the functions' secrets, which are one set per project, into the team's gateways
-- row: the inbox its mail must be addressed to, the address people are told, the From, the provider's name and its
-- connector's send URL. All are public values; the secrets (the connector secret, the providers' keys, the tick
-- token) stay project-wide and are shared by every gateway in the project. A row without settings (installed
-- before this migration) keeps working from the project's BRIGADE_MAIL_* secrets until the installer runs again.
alter table brigade_gateway.gateways
  add column inbox          text check (char_length(inbox) <= 320),
  add column public_address text check (char_length(public_address) <= 320),
  add column from_address   text check (char_length(from_address) <= 320),
  add column provider       text check (char_length(provider) <= 64),
  add column send_url       text check (char_length(send_url) <= 2048);
