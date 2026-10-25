package replicas

import "time"

// CrashLoopSettled is how long a replica runs before its exit no longer counts
// toward the exits in a row CrashLoopBackoff reads: the kubelet resets its
// back-off once a container has run for ten minutes.
const CrashLoopSettled = 10 * time.Minute

// CrashLoopBackoff is the kubelet's crash-loop back-off, the wait before a
// replica that exited consecutive times in a row restarts: 10 seconds,
// doubling with each further exit, to at most 5 minutes.
func CrashLoopBackoff(consecutive int) time.Duration {
	wait := 10 * time.Second
	for range consecutive - 1 {
		wait *= 2
		if wait >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return wait
}
