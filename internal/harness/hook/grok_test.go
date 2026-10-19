package hook

import (
	"bytes"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
)

// No unmeasured hook shape or shared leader PID may silently register a peer.
func TestGrokHooksStayGated(t *testing.T) {
	t.Parallel()
	for _, event := range []string{SubSessionStart, SubPrompt, SubSessionEnd} {
		for _, explicit := range []bool{false, true} {
			args := []string{event}
			env := []string{"HOME=" + t.TempDir(), config.GrokSessionIDVar + "=synthetic"}
			if explicit {
				args = append(args, "--host", "grok", "--plugin-root", "/synthetic")
				env = append(env, "CLAUDE_PID=42")
			}
			var out, errOut bytes.Buffer
			deps := Deps{ReadFile: func(string) ([]byte, error) { t.Fatal("gated hook read configuration"); return nil, nil }, Getppid: func() int { t.Fatal("gated hook inferred parent identity"); return 0 }}
			code := Run(args, cli.Streams{In: strings.NewReader(`{"sessionId":"synthetic","source":"load"}`), Out: &out, Err: &errOut}, env, deps)
			if code != 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "participation is gated") {
				t.Fatalf("%s: exit %d stdout %q stderr %q", event, code, out.String(), errOut.String())
			}
		}
	}
}
