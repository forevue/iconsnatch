package main

import (
	"time"
)

const (
	// 120 requests per minute is 2 requests per second.
	rateLimit = time.Second / 2
	// Allow bursts of up to 5 requests.
	burstLimit = 5
)

// requestThrottle is a channel used to enforce a global rate limit on outgoing requests.
var requestThrottle chan time.Time

func init() {
	requestThrottle = make(chan time.Time, burstLimit)

	go func() {
		ticker := time.NewTicker(rateLimit)
		defer ticker.Stop()
		for t := range ticker.C {
			requestThrottle <- t
		}
	}()
}
