package mirror

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

func createRoundtripper() http.RoundTripper {
	return &http.Transport{
		MaxIdleConns:    1,
		MaxConnsPerHost: 1,
	}
}

func sendRequest(req *http.Request, ctx context.Context, transport http.RoundTripper, timeout time.Duration) error {
	ctx, requestCancel := context.WithTimeout(ctx, timeout)
	defer requestCancel()

	if resp, err := transport.RoundTrip(req.WithContext(ctx)); err == nil {
		// Copy the response body to the bitbucket
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			return fmt.Errorf("reading response failed: %w", err)
		}
		if err = resp.Body.Close(); err != nil {
			return fmt.Errorf("closing response body failed: %w", err)
		}
	} else {
		return fmt.Errorf("sending request failed: %w", err)
	}

	return nil
}

// Algorithm and defaults taken from
// https://github.com/cenkalti/backoff/blob/v4/exponential.go
const retryBaseInterval = float64(500 * time.Millisecond)
const retryMultiplier = 1.5
const retryRandomFactor = 0.5
const retryMaximum = float64(60 * time.Second)

func nextRetryIn(failures int) time.Duration {
	multiplier := float64(max(failures-1, 0))
	base := retryBaseInterval * math.Pow(retryMultiplier, multiplier)
	randomDelta := base * retryRandomFactor
	minInterval := base - randomDelta
	maxInterval := base + randomDelta

	return time.Duration(min(minInterval+(rand.Float64()*(maxInterval-minInterval+1)), retryMaximum))
}

func (m *Mirror) mirrorWorker() {
	failures := 0
	noRequestsUntil := time.Now()
	// Create a RoundTripper to service all requests for this worker
	transport := createRoundtripper()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		select {
		case _, _ = <-m.cancelChan:
			return
		case serReq := <-m.requestChan:
			if failures > 0 && noRequestsUntil.After(time.Now()) {
				continue
			}
			req := serReq.deserialize(m.parsedTarget)
			if err := sendRequest(req, baseCtx, transport, m.RequestTimeout); err != nil {
				failures += 1
				noRequestsUntil = time.Now().Add(nextRetryIn(failures))
				m.logger.Error("Mirror request failed", zap.Error(err), zap.Time("backoff_until", noRequestsUntil))
			} else {
				failures = 0
			}
		}
	}
}
