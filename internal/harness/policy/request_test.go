package policy

import "testing"

// TestTighten pins the typed wrapper of card 126 over the map's ordering
// and ValidRequest's two words.
func TestTighten(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		decided Policy
		request string
		want    Policy
	}{
		{Accept, "", Accept},
		{Accept, "hold", Hold},
		{Accept, "refuse", Refuse},
		{Hold, "", Hold},
		{Hold, "refuse", Refuse},
		{Hold, "accept", Hold},
		{Refuse, "hold", Refuse},
		{Refuse, "", Refuse},
		{Accept, "auto", Accept},
	} {
		if got := Tighten(tc.decided, tc.request); got != tc.want {
			t.Errorf("Tighten(%s, %q) = %s, want %s", tc.decided, tc.request, got, tc.want)
		}
	}
	for s, want := range map[string]bool{"hold": true, "refuse": true, "accept": false, "": false, "HOLD": false, "auto": false} {
		if got := ValidRequest(s); got != want {
			t.Errorf("ValidRequest(%q) = %v, want %v", s, got, want)
		}
	}
}
