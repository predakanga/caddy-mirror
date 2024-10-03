package mirror

import (
	"context"
	"fmt"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

const DROP_ALERT_INTERVAL = 5

func init() {
	caddy.RegisterModule(&Mirror{})
	httpcaddyfile.RegisterHandlerDirective("mirror", parseCaddyfile)
}

type Request struct {
	host    string
	method  string
	headers http.Header
	path    string
	query   string
}

func serializeRequest(r *http.Request) Request {
	return Request{
		r.Host,
		r.Method,
		r.Header.Clone(),
		r.URL.RawPath,
		r.URL.RawQuery,
	}
}

func (r Request) deserialize(baseUrl *url.URL) *http.Request {
	targetUrl := *baseUrl
	targetUrl.RawPath = r.path
	targetUrl.RawQuery = r.query

	return &http.Request{
		Host:       r.host,
		Method:     r.method,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     r.headers,
		URL:        &targetUrl,
	}
}

type Mirror struct {
	SamplingRate       float64       `json:"sampling_rate,omitempty"`
	RequestConcurrency int           `json:"request_concurrency,omitempty"`
	MaxBacklog         int           `json:"max_backlog,omitempty"`
	TargetServer       string        `json:"target,omitempty"`
	RequestTimeout     time.Duration `json:"request_timeout,omitempty"`

	requestChan     chan Request
	cancelChan      chan interface{}
	rng             *rand.Rand
	parsedTarget    *url.URL
	logger          *zap.Logger
	droppedRequests atomic.Uint64
}

var (
	_ caddy.Provisioner           = (*Mirror)(nil)
	_ caddy.Validator             = (*Mirror)(nil)
	_ caddyfile.Unmarshaler       = (*Mirror)(nil)
	_ caddyhttp.MiddlewareHandler = (*Mirror)(nil)
	_ caddy.CleanerUpper          = (*Mirror)(nil)
)

// CaddyModule returns the Caddy module information.
func (*Mirror) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.mirror",
		New: func() caddy.Module { return new(Mirror) },
	}
}

func (m *Mirror) Provision(ctx caddy.Context) error {
	m.EnsureDefaults()
	m.logger = ctx.Logger()
	m.requestChan = make(chan Request, m.MaxBacklog)
	m.cancelChan = make(chan interface{}, 1)
	m.rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	for i := 0; i < m.RequestConcurrency; i++ {
		go m.mirror_worker()
	}

	return nil
}

func (m *Mirror) Validate() error {
	if m.TargetServer == "" {
		return fmt.Errorf("target server is required")
	}
	var err error
	m.parsedTarget, err = url.Parse(m.TargetServer)
	if err != nil {
		return err
	}

	if m.parsedTarget.Scheme == "" {
		m.parsedTarget.Scheme = "http"
		m.logger.Warn("Defaulting to HTTP for mirror target")
	}
	if m.parsedTarget.Scheme != "http" && m.parsedTarget.Scheme != "https" {
		return fmt.Errorf("target scheme must be either http or https")
	}
	if m.parsedTarget.RawPath != "" {
		return fmt.Errorf("path is not supported for mirror targets")
	}
	if m.parsedTarget.RawQuery != "" {
		return fmt.Errorf("query is not supported for mirror targets")
	}

	return nil
}

func (m *Mirror) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	// Consume the directive token
	d.Next()
	// If we have an argument, use that as the target
	if d.NextArg() {
		m.TargetServer = d.Val()
	}
	// Then we expect either EOF or a block
	if d.CountRemainingArgs() > 0 {
		return fmt.Errorf("expected at most a single target and an optional configuration block")
	}
	for d.NextBlock(0) {
		switch d.Val() {
		case "sample":
			if !d.NextArg() {
				return d.ArgErr()
			}
			val, err := strconv.ParseFloat(d.Val(), 64)
			if err != nil {
				return fmt.Errorf("invalid sample rate: %w", err)
			}
			m.SamplingRate = val
		case "concurrency":
			if !d.NextArg() {
				return d.ArgErr()
			}
			val, err := strconv.Atoi(d.Val())
			if err != nil {
				return fmt.Errorf("invalid concurrency: %w", err)
			}
			m.RequestConcurrency = val
		case "backlog":
			if !d.NextArg() {
				return d.ArgErr()
			}
			val, err := strconv.Atoi(d.Val())
			if err != nil {
				return fmt.Errorf("invalid backlog: %w", err)
			}
			m.MaxBacklog = val
		case "timeout":
			if !d.NextArg() {
				return d.ArgErr()
			}
			val, err := time.ParseDuration(d.Val())
			if err != nil {
				return fmt.Errorf("invalid timeout: %w", err)
			}
			m.RequestTimeout = val
		case "target":
			if m.TargetServer != "" {
				return fmt.Errorf("target has already been set")
			}
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.TargetServer = d.Val()
		default:
			return fmt.Errorf("unknown directive: %s", d.Val())
		}
	}

	if m.TargetServer == "" {
		return fmt.Errorf("target server is required")
	}

	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Mirror
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return &m, err
}

func (m *Mirror) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if m.rng.Float64() < m.SamplingRate {
		select {
		case m.requestChan <- serializeRequest(r):
		default:
			if m.droppedRequests.Add(1) == 1 {
				go func() {
					time.Sleep(time.Duration(DROP_ALERT_INTERVAL) * time.Second)
					m.logger.Warn("Dropped requests due to full backlog", zap.Uint64("count", m.droppedRequests.Swap(0)))
				}()
			}
		}
	}
	return next.ServeHTTP(w, r)
}

func (m *Mirror) Cleanup() error {
	close(m.cancelChan)
	close(m.requestChan)

	return nil
}

func create_roundtripper() http.RoundTripper {
	return &http.Transport{
		MaxIdleConns:    1,
		MaxConnsPerHost: 1,
	}
}

func (m *Mirror) mirror_worker() {
	// Create a RoundTripper to service all requests for this worker
	transport := create_roundtripper()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		select {
		case _, _ = <-m.cancelChan:
			return
		case serReq := <-m.requestChan:
			req := serReq.deserialize(m.parsedTarget)
			ctx, requestCancel := context.WithTimeout(baseCtx, m.RequestTimeout)
			//ctx := baseCtx
			if resp, err := transport.RoundTrip(req.WithContext(ctx)); err == nil {
				// Copy the response body to the bitbucket
				if _, err = io.Copy(io.Discard, resp.Body); err != nil {
					m.logger.Error("Failed to read response", zap.Error(err))
				}
				if err = resp.Body.Close(); err != nil {
					m.logger.Error("Failed to close response body", zap.Error(err))
				}
			} else {
				m.logger.Error("Failed to mirror request", zap.Error(err))
			}
			requestCancel()
		}
	}
}

func (m *Mirror) EnsureDefaults() {
	if m.SamplingRate <= 0 || m.SamplingRate >= 1.0 {
		m.SamplingRate = 1.0
	}
	m.RequestConcurrency = max(max(m.RequestConcurrency, runtime.GOMAXPROCS(0)/2), 1)
	if m.MaxBacklog < 1 {
		m.MaxBacklog = m.RequestConcurrency * 128
	}
	if m.RequestTimeout <= 0 {
		m.RequestTimeout = time.Second * 5
	}
}
