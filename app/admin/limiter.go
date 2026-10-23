package admin

import (
	"sync"
	"time"
)

const (
	failureBudget = 5
	failureWindow = time.Minute
)

// limiter runs one argon2id operation at a time and budgets verify failures
// for the whole daemon. Successes are not counted, so a burst of legitimate
// unlocks never locks anyone out.
type limiter struct {
	slot chan struct{}

	mu       sync.Mutex
	failures []time.Time
}

func newLimiter() *limiter {
	return &limiter{slot: make(chan struct{}, 1)}
}

// tryAcquire never waits: a queued verify would make admin_unlock slow.
func (l *limiter) tryAcquire() bool {
	select {
	case l.slot <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *limiter) release() { <-l.slot }

// retryAfter is 0 while a verify may run, otherwise the seconds until the
// oldest failure leaves the window.
func (l *limiter) retryAfter(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	if len(l.failures) < failureBudget {
		return 0
	}
	wait := l.failures[0].Add(failureWindow).Sub(now)
	secs := int((wait + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}

func (l *limiter) fail(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	l.failures = append(l.failures, now)
	if len(l.failures) > failureBudget {
		l.failures = l.failures[len(l.failures)-failureBudget:]
	}
}

func (l *limiter) prune(now time.Time) {
	i := 0
	for i < len(l.failures) && !now.Before(l.failures[i].Add(failureWindow)) {
		i++
	}
	l.failures = l.failures[i:]
}
