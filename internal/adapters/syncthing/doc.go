// Package syncthing is Brigade's third BAP/1 adapter (card 141), bundled in
// the one binary as `brigade adapter syncthing <group> <verb>`. Its backend
// is one directory per team that Syncthing keeps in step between the
// members' machines: the same engine, instance and REST client the sync
// adapter drives for a project's folders (internal/syncthing). There is no
// server, no project and no account. The wire contract is
// docs/protocol-v1.md (BAP/1, frozen); what the protocol leaves to the
// adapter (4.8) — the store layout, the credential, the join handshake —
// is in README.md beside this file.
//
// The store is the test fs adapter's layout (internal/adapters/fs), which
// was written as the dry run for this, with the changes a replicated
// directory needs: every path has one writer, the lock is machine-local
// and lives outside the folder, and a member's revocation is a marker the
// creator writes beside the member's own file, never inside it.
//
// Identity has no authority here (the owner's ruling of 2026-10-26): the
// sender stamps itself, and a member who writes files by hand can claim
// any sender. Membership is device acceptance: a joining machine names
// its device with an HMAC keyed by the join secret's digest, and any
// member's running watch accepts it onto the team folder.
package syncthing
