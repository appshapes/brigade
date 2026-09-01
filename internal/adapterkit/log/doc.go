// Package log is Brigade's one redacting slog handler (plan 7.3, T12):
// every diagnostic line an adapter or the harness writes to stderr or a
// log file goes through it, because the harness captures adapter stderr
// at debug level and a leak here puts a credential in a 0600-but-still-
// on-disk log file (U-09, U-23).
//
// Four layers, applied to every attribute value, attribute key and the
// record message:
//
//  1. Key list: an attribute whose key names a secret (token, secret,
//     authorization, apikey/api_key, password — which contains the plan
//     1315 list: access_token, refresh_token, join_secret; matched
//     case-insensitively, '-' folded to '_', by containment) has its
//     value replaced whatever its kind or content.
//  2. Format patterns inside string values: JWTs (eyJ….…​.…), the
//     brg1.<team_ref>.<secret> join-secret format (D5), sb_secret_ keys
//     and "Bearer …" authorization credentials. Matches are collected as
//     spans on the original string and merged before replacement, and
//     the prefix-literal formats are re-anchored at every occurrence of
//     their literal, so an overlapping earlier match can never consume a
//     real token's prefix and leave its tail exposed.
//  3. Exact tokens: secrets known at run time (the messaging token, a
//     refresh token) registered on the Redactor are replaced wherever
//     they appear, including in the middle of a longer string such as a
//     URL query or a wrapped error message.
//  4. Scalar-only policy: ReplaceAttr cannot see inside a struct, map or
//     slice value (a struct passed through slog.Any prints its fields
//     verbatim [verified, plan A.7]), so slog.Any is banned outside this
//     package by forbidigo (7.3) — the static half — and, as the runtime
//     half, this handler replaces any KindAny value that is not an error
//     with a fixed marker instead of printing it. Errors are logged
//     through Err (or any string attr); everything else is logged as a
//     scalar: slog.String, Int, Bool, Duration, Time. Attributes inside
//     slog.Group are scalars in disguise — the JSON handler walks them
//     and hands each leaf to ReplaceAttr — and the tests pin that walk.
//
// What the handler cannot protect: a secret used as a group NAME in
// WithGroup (ReplaceAttr never sees group names — group names are
// compile-time constants here) and text that never passes through slog.
// For the latter, Redact is exported so raw text (a server error body
// logged at debug) can be scrubbed before any other sink.
package log
