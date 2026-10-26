# Revoke or ban a member

In your own terminal:

```sh
brigade team members --json                             # copy .members[].principal_ref of the member to remove
printf '{"principal_ref":"<ref>"}' | brigade team revoke-member
brigade team revoke-member --principal <ref>            # the same, without stdin
brigade team revoke-member --principal <ref> --ban      # also blocks a rejoin with the current secret
```

- **A revoke** closes that member's open sessions at once (a running watcher of theirs ends within about a second,
  with the same `unauthorized` a `team leave` produces), removes them from the roster and ends their access to
  their inboxes. They may rejoin with the current secret, as the same principal. Every read filters on active
  membership, so it takes effect on every path; one lag of at most an hour: a realtime channel the member had
  already joined keeps receiving message ids, and nothing else, until their access token expires. Measured at
  about 2 ms on both shipped paths.
- **A ban** does all of that and makes the member's rejoin answer *exactly* what a wrong secret answers, so a
  banned member cannot tell it was banned. A banned member is no longer listed by `brigade team members` at all,
  and un-banning (`revoke-member --principal <ref>` without `--ban`) needs the reference.
- **Write the `principal_ref` down before you ban.** The human roster prints only its first characters in
  brackets; `--principal` is passed to the adapter verbatim and needs the reference in full, from `--json`. If it
  is lost, the recovery is a secret rotation plus a fresh principal on the member's side (`brigade team reset`,
  then `brigade team join`).
- You cannot revoke or ban yourself; leaving is `brigade team leave`, reversible by a rejoin.
- The response to a hostile teammate who floods the team within the rate limits is this command.
- **Over Syncthing** ([Create a team over Syncthing](../create/create-over-syncthing.md)) a revoke is a marker your
  machine writes into the team folder. Every member's machine filters on it the moment it arrives, the revoked
  member's own watch ends then with the same `unauthorized`, and every member's engine drops their device after
  a 15-second grace. Measured on one machine, two engines: the watch ended 7.5 s after the revoke, the device was
  dropped 18.5 s after. A revoked machine that is off learns of it when it next connects to a member. A rejoin
  with the current secret, or the revoked machine's own `team join` with it, removes a marker that is not a ban.

The security discussion: [docs/security.md, section 9](../../security.md#9-running-a-team-and-losing-the-ability-to).
