// Command brigade-webhook-gateway runs the Brigade webhook gateway: a team
// member session that forwards its inbox to a signed webhook and accepts
// signed POSTs back. It speaks only BAP/1 through the profile's adapter,
// so it works over the bundled Supabase adapter (which this binary also
// carries, as `brigade-webhook-gateway adapter supabase …`) or any other.
//
// The implementation and its threat notes live in internal/gateway/webhook;
// the operator's page is docs/project-owner/configure/webhook-gateway.md.
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
