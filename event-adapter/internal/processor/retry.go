package processor

import "time"

type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func (p RetryPolicy) Delay(attempt int) time.Duration {
	if attempt <= 1 {
		return p.InitialBackoff
	}
	delay := p.InitialBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= p.MaxBackoff {
			return p.MaxBackoff
		}
	}
	return delay
}
