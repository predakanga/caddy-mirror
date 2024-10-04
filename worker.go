package mirror

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"io"
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

func (m *Mirror) mirrorWorker() {
	// Create a RoundTripper to service all requests for this worker
	transport := createRoundtripper()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		select {
		case _, _ = <-m.cancelChan:
			return
		case serReq := <-m.requestChan:
			req := serReq.deserialize(m.parsedTarget)
			if err := sendRequest(req, baseCtx, transport, m.RequestTimeout); err != nil {
				m.logger.Error("Mirror request failed", zap.Error(err))
			}
		}
	}
}
