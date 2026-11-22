// Command brigade-webhook-gateway runs the Brigade webhook gateway: a team
// member session that forwards its inbox to a signed webhook and accepts
// signed POSTs back. It speaks only BAP/1 through the profile's adapter,
// so it works over the two bundled adapters, for a Supabase project and
// for a Brigade server (which this binary also carries, as
// `brigade-webhook-gateway adapter supabase …` and `… adapter rest …`), or
// any other.
//
// The implementation and its threat notes live in internal/gateway/webhook;
// the operator's page is docs/project-owner/configure/webhook-gateway.md.
//
// x509usefallbackroots=1 is cmd/brigade's directive, for its reason: the
// two bundled adapters link the Mozilla root bundle
// (golang.org/x/crypto/x509roots/fallback), and it is the verifier's
// roots only where package main says so. Without the line a gateway on a
// machine with no CA bundle, a minimal Linux container, could not verify
// the certificate of a Supabase project or of a Brigade server whose
// certificate a public authority signed, where `brigade` on the same
// machine can (work item 389).
//
//go:debug x509usefallbackroots=1
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/appshapes/brigade/internal/gateway/webhook"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := webhook.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ())
	stop()
	os.Exit(code)
}
