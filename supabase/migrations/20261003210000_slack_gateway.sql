-- Brigade Slack gateway migration (Trello card 48, the Slack half; docs/slack-gateway.md). Applied after
-- 20261003160000_mail_gateway.sql. A team may now run a gateway of more than one kind — the mail gateway and
-- a Slack gateway, each an ordinary member with its own session — so the gateways table is keyed by team AND
-- kind, and a thread row says which kind it belongs to, so a Slack lookup (channel + thread ts) never matches an
-- email row (address + Message-ID) and the other way round. `actor` records the person a Slack row is about
-- (their Slack user id, which Slack authenticated), where an email row has only the address.
--
-- The schema stays postgres-only with RLS on and no policy and no grant (the first migration's stance); nothing
-- in the brigade schema changes. Migrations are append-only: the constraints below are replaced by name, the
-- names being the ones PostgreSQL gave the first migration's primary key and check.

alter table brigade_gateway.gateways drop constraint gateways_pkey;
alter table brigade_gateway.gateways add primary key (team_id, kind);
alter table brigade_gateway.gateways drop constraint gateways_kind_check;
alter table brigade_gateway.gateways add constraint gateways_kind_check check (kind in ('email', 'slack'));

alter table brigade_gateway.threads
  add column kind  text not null default 'email' check (kind in ('email', 'slack')),
  add column actor text check (char_length(actor) <= 128);
-- A Slack reply is found by the channel it was posted in and the ts of the thread's root message.
create index threads_slack_thread_idx on brigade_gateway.threads (address, mail_message_id) where kind = 'slack';
