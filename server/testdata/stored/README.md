# What released servers left on the disk

Each directory here is named for a released version of the Brigade server and holds what that server left
behind (work item 385):

- `data/` is its data directory after it stopped: `record.key`, `log/records.log` and `snapshots/`. It ran on
  the file log.
- `entries.jsonl` is that log opened with that key, one entry to a line, so that the stored form of a record
  can be read here and seen in a diff. A test holds that the lines are the bytes the sealed log holds.

Every build is held to every directory. `crates/brigade-core/tests/stored.rs` reads each entry and each
snapshot's state and writes them back the same. `crates/brigade-server/tests/stored.rs` opens the sealed log,
starts the server on the directory, and holds that from the release's older snapshot it reaches the state the
release had. `make server-test` runs both. A member added to a record or to the state without a default fails
them with the member's name, where it would otherwise be a server that does not start on an operator's
directory.

A directory is never changed and never removed: what a release wrote is somebody's data until they say
otherwise.

## What a directory holds

The scenario is `first_run`, `between_the_runs` and `second_run` in `crates/brigade-server/tests/stored.rs`: a
team whose sessions hold pending and acknowledged messages, a session with every fact a registration carries,
a session closed and opened again, a team with every record of its administration, teams taken in by the
operator and by a request, and a team the operator removed.

Its log holds 18 of the 20 kinds of record. The two it does not hold are `team.move_recorded` and
`team.move_cleared`: a recorded move keeps the move ticket whole, in the log and in the state, and a ticket has
the shape of a join secret, which no file of this repository holds. Their bytes are written by hand in
`crates/brigade-core/src/record.rs` and `crates/brigade-server/src/snapshot.rs`.

Nothing here is anyone's secret. `record.key` is not a key a server made: the writer puts the test's own
key there before the released server first starts, as an operator gives a second instance a copy of the
first one's key. It is the 32 bytes `brigade-stored-form-test-key-385`, which are in the test's source for
everyone to read, and a test holds that every directory here is sealed under that key and no other. The log
holds the hashes of credentials and join secrets of a server that ran for two seconds on a developer's
machine, and never a credential or a secret. The join secrets of the two teams that "live elsewhere" are made
up by the scenario.

## Writing the directory of a release

After a server release, from the repository's root, with the version in place of `0.10.0`:

```sh
git worktree add --detach ../brigade-server-0.10.0 server-v0.10.0
(cd ../brigade-server-0.10.0/server && cargo build --release --locked -p brigade-server)
cd server
BRIGADE_RELEASED_SERVER="$PWD/../../brigade-server-0.10.0/server/target/release/brigade-server" \
  BRIGADE_STORED_OUT="$PWD/testdata/stored/0.10.0" \
  cargo test --locked -p brigade-server --test stored -- --ignored write_the_directory
cd .. && git worktree remove ../brigade-server-0.10.0
```

A binary from the GitHub release does as well as one built from the tag. Then run `make server-test` and
commit the directory. The test `every_release_that_is_out_left_its_directory` asks for it from the first
change that puts work under `## [Unreleased]` in `server/CHANGELOG.md` after the release: from then on that
release is out, and a build that nothing holds to what it wrote is what this directory is against.

When a release brings a kind of record, add what makes it to the scenario before writing that release's
directory, and take the kind off `HELD_BY_NO_RELEASE` in `crates/brigade-core/tests/stored.rs`. The earlier
directories stay as they are.

## The directories

- `0.9.0`: written on 2026-11-22 by the server built from the tag `server-v0.9.0` (commit `c009f190`) with
  `cargo build --release --locked`, on macOS, arm64. 45 entries and three snapshots of format 3.
