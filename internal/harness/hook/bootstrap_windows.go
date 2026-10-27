//go:build windows

package hook

// bootstrapName is the plugin's bootstrap under bin/ on Windows: the
// committed launcher (cmd/brigade-launcher, card 133), which is what the
// hooks run and what a terminal runs.
const bootstrapName = "brigade.exe"
