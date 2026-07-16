package bedrock

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

const (
	// maxAttempts bounds total calls to Bedrock at one original plus two retries.
	maxAttempts = 3
	// maxJitter caps the random slice added to each backoff so retrying clients
	// desynchronize instead of firing in lockstep.
	maxJitter = 250 * time.Millisecond
)

// isRetryable reports whether err is a transient Bedrock failure worth retrying:
// the same request, sent again, could plausibly succeed. Throttling and
// transient server-side errors qualify; client-side 4xx errors (validation,
// auth, bad model ID) never do, so an unrecognized error is treated as permanent.
func isRetryable(err error) bool {
	var (
		throttle    *types.ThrottlingException
		unavailable *types.ServiceUnavailableException
		serverErr   *types.InternalServerException
		timeout     *types.ModelTimeoutException
	)
	if errors.As(err, &throttle) || errors.As(err, &unavailable) ||
		errors.As(err, &serverErr) || errors.As(err, &timeout) {
		return true
	}

	// Catch a retryable condition that arrived without its concrete type: every
	// Bedrock error is Smithy-generated, so it exposes a stable ErrorCode().
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ThrottlingException", "ServiceUnavailableException",
			"InternalServerException", "ModelTimeoutException":
			return true
		}
	}

	return false
}

// jitter returns a random duration in [0, maxJitter). Added to the exponential
// backoff, it spreads retries over time so a fleet throttled at the same instant
// does not resynchronize on every round.
func jitter() time.Duration {
	return rand.N(maxJitter)
}

// backoff returns how long to wait before retrying after the given attempt:
// 1s, 2s, 4s, each nudged by jitter. Extracted from withRetry so the schedule
// can be asserted directly instead of by sleeping and measuring a wall clock.
func backoff(attempt int) time.Duration {
	return time.Duration(1<<attempt)*time.Second + jitter()
}

// withRetry calls fn up to maxAttempts times, retrying only transient failures
// with exponential backoff plus jitter. It returns on success, on the first
// permanent error (failing fast with no wait), or when attempts are exhausted
// (returning the last underlying error, so callers can log why it failed). A
// pending wait is abandoned if ctx is cancelled, so a client disconnect stops
// the loop instead of retrying for no one.
func withRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := range maxAttempts {
		err = fn()
		if err == nil || !isRetryable(err) || attempt == maxAttempts-1 {
			return err
		}

		// Sleeping on a select rather than time.Sleep keeps the gap between
		// attempts cancellable: without it, a client that disconnects during
		// backoff still costs a retry.
		select {
		case <-time.After(backoff(attempt)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Unreachable: the loop's last iteration always returns. The compiler can't
	// prove that, so err carries the final result out.
	return err
}
