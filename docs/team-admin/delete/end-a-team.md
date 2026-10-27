# End a team

There is no `brigade team delete`. A team row stays in the backend's `brigade.teams`; what you can end is its use.

1. **Rotate the secret away.** `brigade team rotate-secret --secret-file ~/brigade-<team>.secret`, then delete
   the file. Nobody holds a secret that joins the team.
2. **Evict every other member.** `brigade team revoke-member --max-version <the result's secret_version minus
   one>` closes their sessions and removes them from the roster. A member who should never come back is banned
   by principal ([Revoke or ban a member](../members/revoke-a-member.md)).
3. **Leave and reset.** `brigade team leave`, then `brigade team reset` on your own machine. The reset ends your
   administration, which is the point here.
4. **Remove the file.** Delete `.brigade.json` from every repository that carried it and commit. A checkout that
   still has it connects to nothing once its member is revoked, and its next prompt says to run `brigade team join`
   again.
5. **Tell the Project Owner.** The gateways, if the team had any, keep their functions and their cron job in the
   project until the owner removes them ([Remove a gateway](../../project-owner/remove/remove-a-gateway.md)).

What the backend then does on its own: a closed or expired session and its messages are removed after 7 days;
anonymous principals that hold no membership row are deleted once they are 7 days old. Membership rows and the
team row remain, inert. Deleting the project itself is the owner's [Delete the project](../../project-owner/delete/delete-the-project.md).
