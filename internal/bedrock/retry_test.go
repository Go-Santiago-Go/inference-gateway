package bedrock

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// Transient: the same bytes, sent again, could succeed.
		{"throttling", &types.ThrottlingException{}, true},
		{"service unavailable", &types.ServiceUnavailableException{}, true},
		{"internal server error", &types.InternalServerException{}, true},
		{"model timeout", &types.ModelTimeoutException{}, true},

		// Permanent: the request itself is the problem.
		{"validation", &types.ValidationException{}, false},
		{"access denied", &types.AccessDeniedException{}, false},
		{"model not found", &types.ResourceNotFoundException{}, false},

		// Wrapped, which is how the SDK actually delivers errors. These fail if
		// errors.As is ever replaced with a type assertion.
		{"wrapped throttling", fmt.Errorf("operation error Bedrock: %w", &types.ThrottlingException{}), true},
		{"deeply wrapped throttling", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &types.ThrottlingException{})), true},
		{"wrapped validation", fmt.Errorf("operation error Bedrock: %w", &types.ValidationException{}), false},

		// Untyped API errors: the smithy.APIError fallback classifies by code.
		{"untyped retryable code", &smithy.GenericAPIError{Code: "ThrottlingException"}, true},
		{"untyped permanent code", &smithy.GenericAPIError{Code: "ValidationException"}, false},
		{"untyped unknown code", &smithy.GenericAPIError{Code: "SomeNewException"}, false},

		// Anything unrecognized is treated as permanent: retry only on evidence.
		{"nil", nil, false},
		{"plain error", errors.New("something broke"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryable(tt.err); got != tt.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestBackoffBounds(t *testing.T) {
	for attempt := range maxAttempts {
		base := time.Duration(1<<attempt) * time.Second

		// jitter is random, so a single call could land in range by luck and hide
		// a broken formula. Sample enough to make that vanishingly unlikely.
		for range 1000 {
			got := backoff(attempt)
			if got < base || got >= base+maxJitter {
				t.Fatalf("backoff(%d) = %v, want in [%v, %v)",
					attempt, got, base, base+maxJitter)
			}
		}
	}
}

func TestWithRetry(t *testing.T) {
	throttle := &types.ThrottlingException{}

	t.Run("succeeds on first try", func(t *testing.T) {
		calls := 0
		err := withRetry(context.Background(), func() error {
			calls++
			return nil
		})
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if calls != 1 {
			t.Errorf("called %d times, want 1", calls)
		}
	})

	t.Run("permanent error fails fast", func(t *testing.T) {
		validation := &types.ValidationException{}
		calls := 0
		start := time.Now()

		err := withRetry(context.Background(), func() error {
			calls++
			return validation
		})

		if !errors.Is(err, validation) {
			t.Errorf("err = %v, want the validation error", err)
		}
		if calls != 1 {
			t.Errorf("called %d times, want 1: a 4xx must not be retried", calls)
		}
		// The assertion that matters: fail-fast means no backoff at all.
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Errorf("took %v, want near-instant: fail-fast must not sleep", elapsed)
		}
	})

	t.Run("cancellation abandons the backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0

		err := withRetry(ctx, func() error {
			calls++
			cancel() // the client disconnects while this attempt is in flight
			return throttle
		})

		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		// The point: a retryable error with attempts left, yet no second call.
		// Without the select on ctx.Done(), this would sleep 1s and call again
		// on behalf of a client who already left.
		if calls != 1 {
			t.Errorf("called %d times, want 1: a cancelled wait must not retry", calls)
		}
	})

	t.Run("retries transient failures then succeeds", func(t *testing.T) {
		t.Parallel() // costs ~3s of real backoff (1s + 2s); overlap it
		calls := 0

		err := withRetry(context.Background(), func() error {
			calls++
			if calls < 3 {
				return throttle
			}
			return nil
		})

		if err != nil {
			t.Errorf("err = %v, want nil: the third attempt succeeded", err)
		}
		if calls != 3 {
			t.Errorf("called %d times, want 3", calls)
		}
	})

	t.Run("exhausts attempts and returns the last error", func(t *testing.T) {
		t.Parallel() // costs ~3s of real backoff
		calls := 0

		err := withRetry(context.Background(), func() error {
			calls++
			return throttle
		})

		// The underlying error, not a generic "retries exhausted": logs and
		// on-call need to know it was throttling.
		if !errors.Is(err, throttle) {
			t.Errorf("err = %v, want the throttling error", err)
		}
		if calls != maxAttempts {
			t.Errorf("called %d times, want %d", calls, maxAttempts)
		}
	})
}
