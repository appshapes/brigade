# Name the gateways

A `gateway` member tells people without a session how to reach the team: the mail gateway's address, the Slack
gateway's workspace and bot, or both:

```json
"gateway": { "email": "brigade-team@example.com", "slack": "example.slack.com: @brigade" }
```

You do not write it by hand. The Project Owner's `make gateway-install` and `make slack-gateway-install` write it
into the project's `.brigade.json` when they install a gateway ([Configure the project](../../project-owner/configure/README.md));
you commit the changed file. Both values are public text. A plugin older than 0.18.0 ignores the member and says so
in one line at session start; 0.18.0 knows `email`, 0.19.0 knows `slack`.

What a member does with a gateway: [Write to people on email or Slack](../../team-member/use/people-on-email-and-slack.md).
Who may write to the mail gateway is the Project Owner's `make gateway-senders`
([Choose who may write](../../project-owner/configure/mail-senders.md)): until a list exists, anyone who knows the
address can write to any session of the team.
