package guesttransport

import (
	"context"
	"time"
)

// RunHeartbeat periodically confirms the guest is still responsive by
// calling agent.State, and calls onStatusChange exactly when the guest
// crosses the stuck/responsive boundary - never on every probe, and
// never on a single miss. OpState travels the same authenticated channel
// exec/signal/shutdown already share, so one slow response under real
// load is an inconclusive observation, not a confirmed hang - a
// threshold is what turns "observed" into "confirmed" here (the Twelve
// Commandments' distinction the rest of this codebase already applies
// to process identity and cache validity).
//
// RunHeartbeat blocks until ctx is done; callers run it in its own
// goroutine.
func RunHeartbeat(ctx context.Context, agent *Agent, interval time.Duration, missedThreshold int, onStatusChange func(stuck bool, consecutiveMisses int)) {
	if interval <= 0 || missedThreshold <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutiveMisses := 0
	stuck := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, interval)
			_, err := agent.State(probeCtx)
			cancel()
			if err != nil {
				consecutiveMisses++
			} else {
				consecutiveMisses = 0
			}
			nowStuck := consecutiveMisses >= missedThreshold
			if nowStuck == stuck {
				continue
			}
			stuck = nowStuck
			if onStatusChange != nil {
				onStatusChange(stuck, consecutiveMisses)
			}
		}
	}
}
