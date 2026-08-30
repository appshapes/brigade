---
name: commit
description: Commit, pull, and push changes using make push with a ticket-prefixed message
argument-hint: "[ticket-id]"
---

Commit the current working tree changes using this project's `make push` target.

`make push` = `make commit` + `git push`. `make commit` alone typechecks, pulls, builds, tests,
ownership-lints, stages and commits, but does NOT push — so this skill uses `make push`.

The ticket ID is: $ARGUMENTS

## 1. Validate the ticket ID

If `$ARGUMENTS` is empty or non-numeric, stop and ask the user to provide a numeric ticket ID (e.g., `/commit 1234`). Do not proceed.

## 2. Analyze changes

Run these commands in parallel to understand what will be committed:

- `git status` (never use `-uall`)
- `git diff`
- `git diff --cached`
- `git log --oneline -5`

If the working tree is clean (no staged, unstaged, or untracked changes), inform the user there is nothing to commit and stop.

Remember: `make push` runs `git add --verbose :/ .` before committing, so ALL working tree changes will be included, not just staged files. It also runs `git pull` BETWEEN typecheck and build, so the build/test/ownership gates run against the MERGED tree rather than a stale one.

## 3. Check for secrets

Scan every added line (`+` prefix) in the diff output — in ALL files, not just code files — for anything that looks like a secret: API keys, passwords, tokens, connection strings, private keys, or credentials. Non-code files (markdown, prompts, config notes) are just as likely to contain secrets. If found, warn the user and do not proceed until they confirm the files are safe.

## 4. Generate the commit message

Based on the changes, write a commit message:

- Format: `<ticket-id>: <Message>` (e.g., `1262: Drizzle schema introspection`)
- One line, under 72 characters when possible
- Imperative mood, capitalized first word after the ticket prefix
- Summarize the intent (why), not a list of files changed
- Avoid shell-special characters (quotes, backticks, dollar signs) in the message

## 5. Confirm with the user

Present the full make command that will be executed:

```
make push "message=<ticket-id>: <generated message>"
```

Ask the user to confirm or provide an edited message. This operation typechecks, builds, tests, ownership-lints, commits, pulls, and pushes -- never auto-execute.

## 6. Execute

Once confirmed, run exactly:

```
make push "message=<confirmed message>"
```

## 7. Report the result

- On success, confirm the commit was pushed.
- On failure (build error, test failure, lint issue, merge conflict), report the error clearly. Do not retry automatically.
