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

/// The owner of a team as a line names it: the member whose request the line is about, because only a
/// team's owner is answered a request that opens or closes its move.
fn owner(state: &State, team_ref: &str) -> String {
    match state.teams.get(team_ref).and_then(|team| team.owner()) {
        Some(owner) => format!("member {}", plain(owner)),
        None => "its owner".to_owned(),
    }
}

/// The lines for the records of one request, read against the state those records were applied to: a team
/// that a member created or took in from another backend, a member who joined, with the team's move ticket
/// when that is what let it in, a take-in that its member made again with the key it kept, a team's new
/// owner, a move that the team's owner opened, a move that its owner or a rotation of the team's join
/// secret closed, and the record of where a team went, made or cleared by its owner. Every other record
/// has none.
#[must_use]
pub fn of_entry(state: &State, entry: &Entry) -> Vec<String> {
    // One request alone makes a credential unknown and issues another in the same entry: a take-in made
    // again with its retry key, whose first answer was lost (the contract's section 3b). The principal
    // of the new credential is the one that take-in made. It always has a credential to make unknown: a
    // principal that gave its own up ended its key by that, and the same request is a stranger's join.
    let replaced = entry
        .records
        .iter()
        .any(|record| matches!(record.event, Event::CredentialRevoked { .. }));
    let repeated = entry.records.iter().find_map(|record| match &record.event {
        Event::CredentialIssued { principal_ref, .. } if replaced => Some(principal_ref.as_str()),
        _ => None,
    });
    // A move is closed by a rotation in the rotation's own entry, and by its owner in an entry of its own.
    let rotated = entry
        .records
        .iter()
        .any(|record| matches!(record.event, Event::TeamSecretRotated { .. }));
    entry
        .records
        .iter()
        .filter_map(|record| match &record.event {
            Event::TeamCreated {
                team_ref,
                created_by,
                taken_in,
                ..
            } if !created_by.is_empty() => Some(if *taken_in {
                // The line says how the team came and never shows what it came with.
                format!(
                    "member {} took in {} with the join secret it has on another backend, and owns it",
                    plain(created_by),
                    team(state, team_ref)
                )
            } else {
                format!("member {} created {}", plain(created_by), team(state, team_ref))
            }),
            // The line says that the credential was replaced and shows neither credential, nor the key,
            // nor the hash of any of them. Who took a team over by a key is what an operator looks for.
            Event::MemberJoined {
                team_ref,
                principal_ref,
                ..
            } if repeated == Some(principal_ref.as_str()) => Some(format!(
                "member {} made its take-in of {} again with the retry key of the first: it has a new \
                 credential, and the one it had is not known any more",
                plain(principal_ref),
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
            // The operator's command that opens a move prints its own line; this is the owner's request.
            // The line names the day the ticket ends and nothing of the ticket.
            Event::TeamMoveOpened {
                team_ref, ends_at, ..
            } => Some(format!(
                "{} opened the move of {}: its ticket admits until {}",
                owner(state, team_ref),
                team(state, team_ref),
                ends_at.rfc3339()
            )),
            // In a member's own request a rotation closes a move, or the team's owner does: the
            // operator's command that closes one prints its own line.
            Event::TeamMoveClosed { team_ref, .. } if rotated => Some(format!(
                "the move of {} is closed by the rotation of its join secret: its ticket admits nobody from now on",
                team(state, team_ref)
            )),
            Event::TeamMoveClosed { team_ref, .. } => Some(format!(
                "{} closed the move of {}: its ticket admits nobody from now on",
                owner(state, team_ref),
                team(state, team_ref)
            )),
            // Where a team went is what an operator looks for when its members stop arriving. The line
            // names the backend as the record does and never shows the ticket the record holds.
            Event::TeamMoveRecorded {
                team_ref,
                adapter,
                url,
                certificate,
                ..
            } => {
                let pinned = if certificate.is_empty() {
                    String::new()
                } else {
                    format!(", certificate {}", plain(certificate))
                };
                Some(format!(
                    "{} recorded that {} moved to {} (adapter {}{pinned}): its members' sessions follow it \
                     there, and the team stays here as it is",
                    owner(state, team_ref),
                    team(state, team_ref),
                    plain(url),
                    plain(adapter)
                ))
            }
            // The operator's command that takes a team back prints its own line; this is the owner's
            // request.
            Event::TeamMoveCleared { team_ref } => Some(format!(
                "{} cleared the record of where {} went: no session follows it from here from now on",
                owner(state, team_ref),
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
    use brigade_core::state::{Member, TakeInRetry, Team};
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
            take_in_retry: None,
            moved_to: None,
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
            retry_key_hash: String::new(),
        }
    }

    fn followed(principal: &str) -> Event {
        Event::MemberJoined {
            team_ref: "t-1".to_owned(),
            principal_ref: principal.to_owned(),
            human_label: String::new(),
            by_move_ticket: true,
            retry_key_hash: String::new(),
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
                    taken_in: false,
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
        // A move that a rotation closed has a line, and it holds no hash.
        let lines = of_entry(
            &state(),
            &entry(vec![
                Event::TeamSecretRotated {
                    team_ref: "t-1".to_owned(),
                    join_secret_hash: "hash-of-the-secret".to_owned(),
                    secret_version: 2,
                },
                Event::TeamMoveClosed {
                    team_ref: "t-1".to_owned(),
                    by_owner: false,
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
    fn a_team_taken_in_and_a_move_its_owner_opened_or_closed_each_have_a_line() {
        // A team a member took in by request says so, and that the member owns it: the operator reads
        // here who put a team on the server.
        let lines = of_entry(
            &state(),
            &entry(vec![
                Event::TeamCreated {
                    team_ref: "t-1".to_owned(),
                    team_name: "ops".to_owned(),
                    join_secret_hash: "hash-of-the-secret".to_owned(),
                    created_by: "p-alice".to_owned(),
                    taken_in: true,
                },
                joined("p-alice", "alice@example.com"),
            ]),
        );
        assert_eq!(
            lines,
            [
                "member p-alice took in team ops (t-1) with the join secret it has on another backend, and \
              owns it",
                "member p-alice joined team ops (t-1), labelled \"alice@example.com\"",
            ]
        );
        // The move its owner opens names the owner and the ticket's end, and never the ticket's hash.
        let lines = of_entry(
            &state(),
            &entry(vec![Event::TeamMoveOpened {
                team_ref: "t-1".to_owned(),
                ticket_hash: "hash-of-the-ticket".to_owned(),
                ends_at: Timestamp(1_800_000_000_000_000_000),
                by_owner: true,
            }]),
        );
        assert_eq!(
            lines,
            [
                "member p-alice opened the move of team ops (t-1): its ticket admits until \
              2027-01-15T08:00:00Z"
            ]
        );
        // The move its owner closes is not one a rotation closed, and the line says whose request it was.
        let lines = of_entry(
            &state(),
            &entry(vec![Event::TeamMoveClosed {
                team_ref: "t-1".to_owned(),
                by_owner: true,
            }]),
        );
        assert_eq!(
            lines,
            ["member p-alice closed the move of team ops (t-1): its ticket admits nobody from now on"]
        );
    }

    #[test]
    fn the_record_of_where_a_team_went_has_a_line_and_no_ticket() {
        use brigade_core::record::KeptTicket;

        // The owner's record names where the team went, as the record does, and holds nothing of the
        // ticket: the journal is read by more people than the data directory is.
        // The ticket is put together here: no file of the repository holds the shape of one.
        let ticket = concat!("brg1", ".t-1.", "the-secret-part-of-the-ticket");
        let recorded = |certificate: &str| Event::TeamMoveRecorded {
            team_ref: "t-1".to_owned(),
            adapter: "rest".to_owned(),
            url: "https://brigade.example.com:8743".to_owned(),
            certificate: certificate.to_owned(),
            ticket: KeptTicket::new(ticket),
        };
        let lines = of_entry(&state(), &entry(vec![recorded("")]));
        assert_eq!(
            lines,
            [
                "member p-alice recorded that team ops (t-1) moved to https://brigade.example.com:8743 \
              (adapter rest): its members' sessions follow it there, and the team stays here as it is"
            ]
        );
        let lines = of_entry(&state(), &entry(vec![recorded("sha256:00ff")]));
        assert_eq!(
            lines,
            [
                "member p-alice recorded that team ops (t-1) moved to https://brigade.example.com:8743 \
              (adapter rest, certificate sha256:00ff): its members' sessions follow it there, and the team \
              stays here as it is"
            ]
        );
        assert!(lines.iter().all(|line| !line.contains("the-secret-part")));
        // A destination its owner wrote stays on the one line it was written to.
        let lines = of_entry(
            &state(),
            &entry(vec![Event::TeamMoveRecorded {
                team_ref: "t-1".to_owned(),
                adapter: "re\nst".to_owned(),
                url: "https://a\nmember joined".to_owned(),
                certificate: String::new(),
                ticket: KeptTicket::new(ticket),
            }]),
        );
        assert!(lines.iter().all(|line| !line.contains('\n')), "{lines:?}");
        let lines = of_entry(
            &state(),
            &entry(vec![Event::TeamMoveCleared {
                team_ref: "t-1".to_owned(),
            }]),
        );
        assert_eq!(
            lines,
            [
                "member p-alice cleared the record of where team ops (t-1) went: no session follows it from \
              here from now on"
            ]
        );
    }

    #[test]
    fn a_take_in_made_again_with_its_key_has_a_line_and_no_hash() {
        // The entry of a take-in made again: the credential the lost answer carried is unknown, a new one
        // is issued, and the member is the member it was, with the key's hash kept once more.
        let again = entry(vec![
            Event::CredentialRevoked {
                credential_hash: "hash-of-the-lost-credential".to_owned(),
            },
            Event::CredentialIssued {
                principal_ref: "p-alice".to_owned(),
                credential_hash: "hash-of-the-new-credential".to_owned(),
            },
            Event::MemberJoined {
                team_ref: "t-1".to_owned(),
                principal_ref: "p-alice".to_owned(),
                human_label: String::new(),
                by_move_ticket: false,
                retry_key_hash: "hash-of-the-retry-key".to_owned(),
            },
        ]);
        let lines = of_entry(&state(), &again);
        assert_eq!(
            lines,
            [
                "member p-alice made its take-in of team ops (t-1) again with the retry key of the first: \
              it has a new credential, and the one it had is not known any more"
            ]
        );
        assert!(lines.iter().all(|line| !line.contains("hash-of")), "{lines:?}");
        // A first take-in that kept a key is a take-in and a join, as any is: the line of a repeat is for
        // an entry that made a credential unknown, and the key's hash is in no line.
        let first = entry(vec![
            Event::CredentialIssued {
                principal_ref: "p-alice".to_owned(),
                credential_hash: "hash-of-the-credential".to_owned(),
            },
            Event::MemberJoined {
                team_ref: "t-1".to_owned(),
                principal_ref: "p-alice".to_owned(),
                human_label: String::new(),
                by_move_ticket: false,
                retry_key_hash: "hash-of-the-retry-key".to_owned(),
            },
        ]);
        let lines = of_entry(&state(), &first);
        assert_eq!(
            lines,
            ["member p-alice joined team ops (t-1), labelled \"alice@example.com\""]
        );
        // A member that makes its own credential unknown, and one that joins, each in an entry of its
        // own, say nothing of a repeat.
        let revoked = entry(vec![Event::CredentialRevoked {
            credential_hash: "hash-of-the-credential".to_owned(),
        }]);
        assert!(of_entry(&state(), &revoked).is_empty());
    }

    #[test]
    fn only_an_entry_that_makes_a_credential_unknown_is_a_take_in_made_again() {
        // A team that keeps a key for its owner. A principal that gave its credential up ended its key by
        // that, so a take-in made again always has a credential to make unknown, and an entry that makes
        // none unknown is a join, whoever it names and whatever the team keeps.
        let mut keeps = state();
        keeps.teams.get_mut("t-1").unwrap().take_in_retry = Some(TakeInRetry {
            key_hash: "hash-of-the-retry-key".to_owned(),
            principal_ref: "p-alice".to_owned(),
        });
        let issued_and_joined = |principal: &str, retry_key_hash: &str| {
            entry(vec![
                Event::CredentialIssued {
                    principal_ref: principal.to_owned(),
                    credential_hash: "hash-of-the-new-credential".to_owned(),
                },
                Event::MemberJoined {
                    team_ref: "t-1".to_owned(),
                    principal_ref: principal.to_owned(),
                    human_label: String::new(),
                    by_move_ticket: false,
                    retry_key_hash: retry_key_hash.to_owned(),
                },
            ])
        };
        // A stranger's join, the first take-in with a key, and the two records of a principal that is
        // issued a credential with none made unknown: each joined.
        assert_eq!(
            of_entry(&keeps, &issued_and_joined("p-bob", "")),
            ["member p-bob joined team ops (t-1), with no label"]
        );
        for retry_key_hash in ["hash-of-the-retry-key", ""] {
            let lines = of_entry(&keeps, &issued_and_joined("p-alice", retry_key_hash));
            assert_eq!(
                lines,
                ["member p-alice joined team ops (t-1), labelled \"alice@example.com\""]
            );
        }
        // A member that joins again with the credential it holds is issued none, and joined.
        assert_eq!(
            of_entry(&keeps, &entry(vec![joined("p-alice", "")])),
            ["member p-alice joined team ops (t-1), labelled \"alice@example.com\""]
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
                    taken_in: false,
                },
                // A caller that may not own takes a team in with no owner, and has no line either.
                Event::TeamCreated {
                    team_ref: "t-1".to_owned(),
                    team_name: "ops".to_owned(),
                    join_secret_hash: "hash-of-the-secret".to_owned(),
                    created_by: String::new(),
                    taken_in: true,
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
