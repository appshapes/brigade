# Developer

You work on Brigade itself: a Go module with one shipped binary, a package per harness, a Supabase backend, a frozen
protocol and a conformance suite, developed on `master` through one gate that every push and every release passes.

## If you want to

| If you want to | Go to |
| --- | --- |
| set up a clone, the local stack, or the plugin against your own build | [Set up](setup/README.md) |
| run the gate, the tests, the proofs, or read what CI does | [Build and test](build-and-test/README.md) |
| change the protocol, an adapter, the backend, a package, or add a harness | [Change](change/README.md) |
| cut a release and send its notes | [Release](release/README.md) |
| commit, work a card, hand work to the agentic loop | [Contribute](contribute/README.md) |
| add or change documentation, and keep the gate green | [Documentation](documentation/README.md) |
| read the experiments, the research, the proof results and the execution log | [Evidence](evidence/README.md) |

## Set up

Go 1.27.2, `make`, `git`, `curl`, then `make setup`. [Set up](setup/README.md) adds the local Supabase stack and
running the plugin from the checkout.

## Build and test

`make typecheck lint build test vuln deps-check schema-check tidy-check docs-check` is the acceptance gate, Docker-free.
[Build and test](build-and-test/README.md) covers the live suites, the proofs and CI.

## Change

The protocol is frozen, the harness never imports the adapter, stdout is protocol output, and a harness is a
registry entry plus its pages. [Change](change/README.md) has each surface.

## Release

`make release version=<v> card=<n>` pins, builds reproducibly, commits and tags; the workflow verifies and
publishes. [Release](release/README.md) includes the notes email and what the owner does after.

## Contribute

`<card>: <Imperative summary>` through `make push`, merges only, a Trello card per piece of work.
[Contribute](contribute/README.md) also covers the agentic loop.

## Documentation

One tree for humans, one for agents, and a gate that fails a release without its pages.
[Documentation](documentation/README.md) is the structure, the gate and how to add a harness.

## Evidence

Every number in the documentation has a dated write-up. [Evidence](evidence/README.md) points at them.
