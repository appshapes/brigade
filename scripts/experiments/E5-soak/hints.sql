-- E5-soak hint generator (plan row P5-11, brief 5.2 / M3 / B1): the SAME realtime.send call the shipped
-- trigger makes on every accepted message (supabase/migrations/20260830120100_brigade_realtime.sql:26-27),
-- with fabricated message ids and NO backing brigade.messages row. Run as postgres inside the database
-- container (the house way, Makefile `docker exec -i supabase_db_brigade psql -U postgres -d postgres`):
-- a client cannot broadcast on the topic (no insert policy on realtime.messages; realtime.send downgrades a
-- refused insert to a WARNING, :19-21 and :50), so the database is the only honest instrument (M4).
--
-- psql variables:  sid  the recipient Brigade session id (the topic is brigade:session:<sid>)
--                  n    hints per invocation (B1 paces 10 invocations of 100, one second apart)
--
--   docker exec -i supabase_db_brigade psql -U postgres -d postgres -At -F '|' -v ON_ERROR_STOP=1 \
--     -v sid=<uuid> -v n=100 < scripts/experiments/E5-soak/hints.sql
--
-- Output (three -At rows): t0|<clock>, sent|<count>, t1|<clock>. The rows land in realtime.messages
-- (topic, event, inserted_at), which is where B1 counts them server-side. B1 runs this unit ten times in ONE
-- psql script with `select 'sleep', pg_sleep(<seconds/10>)` between the copies, so the pacing is the server's
-- clock (ten `docker exec` invocations cost ~1.6 s of startup each and stretched 100 hints over 16 s).
select 't0', clock_timestamp();
select 'sent', count(*)
from (select realtime.send(jsonb_build_object('message_id', gen_random_uuid(), 'seq', g),
                           'message_accepted', 'brigade:session:' || :'sid', true)
      from generate_series(1, :n) g) s;
select 't1', clock_timestamp();
