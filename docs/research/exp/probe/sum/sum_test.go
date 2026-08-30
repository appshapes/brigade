package sum

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestAdd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ a, b, want int }{{1, 2, 3}, {0, 0, 0}, {-1, 1, 0}} {
		if got := Add(tc.a, tc.b); got != tc.want {
			t.Errorf("Add(%d,%d)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestBucketSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &Bucket{}
		for i := range 10 {
			if !b.Take(time.Now()) {
				t.Fatalf("take %d refused", i)
			}
		}
		if b.Take(time.Now()) {
			t.Fatal("11th take should be refused")
		}
		time.Sleep(61 * time.Second) // fake clock: returns instantly inside the bubble
		if !b.Take(time.Now()) {
			t.Fatal("after the window a take should succeed")
		}
	})
}
