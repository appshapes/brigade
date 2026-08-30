package sum

import "time"

func Add(a, b int) int { return a + b }

// Bucket is a toy token bucket to exercise synctest's fake clock.
type Bucket struct {
	tokens int
	last   time.Time
}

func (b *Bucket) Take(now time.Time) bool {
	if now.Sub(b.last) >= time.Minute {
		b.tokens = 10
		b.last = now
	}
	if b.tokens == 0 {
		return false
	}
	b.tokens--
	return true
}
