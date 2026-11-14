//! What the server says of what it recorded for a request, and of what its gateways were asked.
//!
//! A line names a team, a member's reference and the label the team's roster shows for that member, or a
//! gateway and the status it answered with. A reason is one of the fixed sentences below, chosen by the
//! status: nothing a gateway answered with is repeated, because some of it comes from the request. No line
//! carries a credential, a join secret, a token or a message's text (work item 313).

use brigade_core::record::Event;
use brigade_core::{Entry, State};

/// The most of a name, a reference or a label that a line carries.
const MOST: usize = 128;

/// A name, a reference or a label as a line may carry it: cut at [`MOST`] characters, with a `?` in place
/// of each control character, so that what a member chose stays on the one line it was written to.
#[must_use]
pub fn plain(text: &str) -> String {
    text.chars()
        .take(MOST)
        .map(|character| if character.is_control() { '?' } else { character })
        .collect()
}

/// A team as a line names it: its name as the state has it, and its reference.
fn team(state: &State, team_ref: &str) -> String {
    match state.teams.get(team_ref) {
        Some(team) => format!("team {} ({})", plain(&team.team_name), plain(team_ref)),
        None => format!("a team ({})", plain(team_ref)),
    }
}

/// The lines for the records of one request, read against the state those records were applied to: a team
/// that a member created, a member who joined, with the team's move ticket when that is what let it in, a
/// team's new owner, and a move that a rotation of the team's join secret closed. Every other record has
/// none.
#[must_use]
pub fn of_entry(state: &State, entry: &Entry) -> Vec<String> {
    entry
        .records
        .iter()
        .filter_map(|record| match &record.event {
            Event::TeamCreated {
                team_ref, created_by, ..
            } if !created_by.is_empty() => Some(format!(
                "member {} created {}",
                plain(created_by),
                team(state, team_ref)
            )),
            Event::MemberJoined {
                team_ref,
                principal_ref,
                by_move_ticket,
                ..
            } => {
                // The label is the roster's: a member who joins again with none keeps the one it had.
                let label = state
                    .teams
                    .get(team_ref)
                    .and_then(|team| team.members.get(principal_ref))
                    .map_or("", |member| member.human_label.as_str());
                let labelled = if label.is_empty() {
                    "with no label".to_owned()
                } else {
                    format!("labelled \"{}\"", plain(label))
                };
                // A member that followed its team here from another backend came in with the move ticket,
                // which the line says and never shows.
                let with = if *by_move_ticket {
                    " with its move ticket"
                } else {
                    ""
                };
                Some(format!(
                    "member {} joined {}{with}, {labelled}",
                    plain(principal_ref),
                    team(state, team_ref)
                ))
            }
            Event::TeamOwnerChanged {
                team_ref,
                principal_ref,
            } if !principal_ref.is_empty() => Some(format!(
                "member {} owns {} from now on",
                plain(principal_ref),
                team(state, team_ref)
            )),
            // In a member's own request only a rotation closes a move: the operator's command that closes
            // one prints its own line.
            Event::TeamMoveClosed { team_ref } => Some(format!(
                "the move of {} is closed by the rotation of its join secret: its ticket admits nobody from now on",
                team(state, team_ref)
            )),
            _ => None,
        })
        .collect()
}

/// The line for one request to a gateway's endpoint: which gateway, whether it took the request, and for
/// one it did not take, why, by the status it answered with.
#[must_use]
pub fn of_gateway(gateway: &str, status: u16) -> String {
    let did = match status {
        200..=299 => "took a request",
        400 => "refused a request it could not read",
        401 if gateway == "slack" => "refused a request its Slack app did not sign",
        401 => "refused a request that does not carry its secret",
        405 => "refused a request that is not a POST",
        408 => "refused a request whose body did not arrive in time",
        413 => "refused a request whose body is too long",
        503 => "was too busy for a request, which is to be sent again",
        500..=599 => "failed at a request",
        _ => "refused a request",
    };
    format!("the {gateway} gateway {did} ({status})")
}

#[cfg(test)]
mod tests {
    use brigade_core::record::Record;
    use brigade_core::state::{Member, Team};
    use brigade_core::{Entry, State, Timestamp};

    use super::*;

    fn member(label: &str) -> Member {
        Member {
            human_label: label.to_owned(),
            active: true,
            joined_at: Timestamp(1),
            joined_secret_version: 1,
            banned: false,
            sent_at: Default::default(),
        }
    }

    fn state() -> State {
        let mut state = State::default();
        let mut team = Team {
            team_ref: "t-1".to_owned(),
            team_name: "ops".to_owned(),
            join_secret_hash: "hash-of-the-secret".to_owned(),
            created_by: "p-alice".to_owned(),
            created_at: Timestamp(1),
            secret_version: 1,
            move_ticket: None,
            members: Default::default(),
            sessions: Default::default(),
        };
        team.members
            .insert("p-alice".to_owned(), member("alice@example.com"));
        team.members.insert("p-bob".to_owned(), member(""));
        team.members.insert(
            "p-eve".to_owned(),
            member("eve\n2026-01-01T00:00:00Z brigade-server: stopping"),
        );
        state.teams.insert("t-1".to_owned(), team);
        state
    }

    fn entry(events: Vec<Event>) -> Entry {
        Entry {
            sequence: 1,
            records: events
                .into_iter()
                .map(|event| Record::new(Timestamp(1), event))
                .collect(),
        }
    }

    fn joined(principal: &str, label: &str) -> Event {
        Event::MemberJoined {
            team_ref: "t-1".to_owned(),
            principal_ref: principal.to_owned(),
            human_label: label.to_owned(),
            by_move_ticket: false,
        }
    }

    fn followed(principal: &str) -> Event {
        Event::MemberJoined {
            team_ref: "t-1".to_owned(),
            principal_ref: principal.to_owned(),
            human_label: String::new(),
            by_move_ticket: true,
        }
    }

    #[test]
    fn a_creation_a_join_and_a_new_owner_each_have_a_line_and_nothing_else_has() {
        let lines = of_entry(
            &state(),
            &entry(vec![
                Event::CredentialIssued {
                    principal_ref: "p-alice".to_owned(),
                    credential_hash: "hash-of-the-credential".to_owned(),
                },
                Event::TeamCreated {
                    team_ref: "t-1".to_owned(),
                    team_name: "ops".to_owned(),
                    join_secret_hash: "hash-of-the-secret".to_owned(),
                    created_by: "p-alice".to_owned(),
                },
                joined("p-alice", "alice@example.com"),
                Event::TeamOwnerChanged {
                    team_ref: "t-1".to_owned(),
                    principal_ref: "p-alice".to_owned(),
                },
            ]),
        );
        assert_eq!(
            lines,
            [
                "member p-alice created team ops (t-1)",
                "member p-alice joined team ops (t-1), labelled \"alice@example.com\"",
                "member p-alice owns team ops (t-1) from now on",
            ]
        );
        // Not even a hash of a secret or of a credential is in a line.
        assert!(lines.iter().all(|line| !line.contains("hash-of")), "{lines:?}");
    }

    #[test]
    fn a_join_names_the_label_the_roster_has_and_says_when_there_is_none() {
        // A member who joins again with no label keeps the one the roster has.
        assert_eq!(
            of_entry(&state(), &entry(vec![joined("p-alice", "")])),
            ["member p-alice joined team ops (t-1), labelled \"alice@example.com\""]
        );
        assert_eq!(
            of_entry(&state(), &entry(vec![joined("p-bob", "")])),
            ["member p-bob joined team ops (t-1), with no label"]
        );
    }

    #[test]
    fn a_join_with_the_move_ticket_says_so_and_a_move_a_rotation_closed_has_a_line() {
        assert_eq!(
            of_entry(&state(), &entry(vec![followed("p-alice"), followed("p-bob")])),
            [
                "member p-alice joined team ops (t-1) with its move ticket, labelled \"alice@example.com\"",
                "member p-bob joined team ops (t-1) with its move ticket, with no label",
            ]
        );
        // The move's own records: one that was opened has no line here, because only the operator opens
        // one and the command prints its own; one that a rotation closed has, and it holds no hash.
        let lines = of_entry(
            &state(),
            &entry(vec![
                Event::TeamMoveOpened {
                    team_ref: "t-1".to_owned(),
                    ticket_hash: "hash-of-the-ticket".to_owned(),
                    ends_at: Timestamp(9),
                },
                Event::TeamSecretRotated {
                    team_ref: "t-1".to_owned(),
                    join_secret_hash: "hash-of-the-secret".to_owned(),
                    secret_version: 2,
                },
                Event::TeamMoveClosed {
                    team_ref: "t-1".to_owned(),
                },
            ]),
        );
        assert_eq!(
            lines,
            [
                "the move of team ops (t-1) is closed by the rotation of its join secret: its ticket admits \
              nobody from now on"
            ]
        );
    }

    #[test]
    fn a_label_cannot_start_a_line_of_its_own() {
        let lines = of_entry(&state(), &entry(vec![joined("p-eve", "")]));
        assert_eq!(lines.len(), 1);
        assert!(!lines[0].contains('\n'), "{lines:?}");
        assert!(lines[0].contains("eve?2026-01-01T00:00:00Z"), "{lines:?}");
        assert_eq!(plain(&"x".repeat(500)).chars().count(), 128);
    }

    #[test]
    fn a_team_that_was_taken_in_and_one_left_with_no_owner_have_no_line() {
        let lines = of_entry(
            &state(),
            &entry(vec![
                Event::TeamCreated {
                    team_ref: "t-1".to_owned(),
                    team_name: "ops".to_owned(),
                    join_secret_hash: "hash-of-the-secret".to_owned(),
                    created_by: String::new(),
                },
                Event::TeamOwnerChanged {
                    team_ref: "t-1".to_owned(),
                    principal_ref: String::new(),
                },
            ]),
        );
        assert!(lines.is_empty(), "{lines:?}");
    }

    #[test]
    fn a_gateways_line_says_which_gateway_what_it_did_and_the_status() {
        assert_eq!(of_gateway("slack", 200), "the slack gateway took a request (200)");
        assert_eq!(
            of_gateway("slack", 401),
            "the slack gateway refused a request its Slack app did not sign (401)"
        );
        assert_eq!(
            of_gateway("mail", 401),
            "the mail gateway refused a request that does not carry its secret (401)"
        );
        assert_eq!(
            of_gateway("mail", 413),
            "the mail gateway refused a request whose body is too long (413)"
        );
        assert_eq!(
            of_gateway("slack", 503),
            "the slack gateway was too busy for a request, which is to be sent again (503)"
        );
        assert_eq!(
            of_gateway("mail", 500),
            "the mail gateway failed at a request (500)"
        );
        assert_eq!(
            of_gateway("mail", 403),
            "the mail gateway refused a request (403)"
        );
    }
}
