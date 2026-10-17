# Stop the keep-alive

The keep-alive is nothing but a job in `keepalive.yml` reading a pair of repository variables. Remove the pair and
the job becomes a green no-op that says so on the run page:

```sh
gh variable delete BRIGADE_SUPABASE_URL
gh variable delete BRIGADE_SUPABASE_PUBLISHABLE_KEY
```

Use the pair named for the project ([Arm the keep-alive](../install/keep-alive.md) lists them). Remove **both**:
exactly one set fails the run on purpose. To remove the job itself, delete it from `keepalive.yml` and from the
table on that page; `scripts/ci/keepalive_test.go` pins the two against each other.

A free-plan project with no keep-alive pauses after about seven days of inactivity and can be resumed from the
dashboard for up to a year.
