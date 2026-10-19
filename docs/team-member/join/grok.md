# Join status for Grok Build

Grok Build participation is experimental. This source tree does not register Grok sessions or deliver team messages; native identity, session-specific liveness and admission remain gated.

Joining a backend membership and attaching a live conversation are separate operations. A human may join
through the ordinary terminal CLI in the checkout with `brigade team join --secret-file <absolute-path>`.
The administrator supplies that secret file outside the repository; never paste or print its contents.
[Joining in a terminal](claude-code.md) describes consent and checkout binding.

The Grok hook currently stops before registration. A successful terminal join therefore does not put this
Grok conversation on the roster or enable receipt. In-session joining must wait for verified hook/tool native
identity and start facts. Do not repeat joining to work around a missing session map.
