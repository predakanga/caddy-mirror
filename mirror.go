package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/petermattis/goid"
	"go.uber.org/zap"
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

// cloneRequest makes a semi-deep clone of origReq.
//
// Copied from https://github.com/caddyserver/caddy/blob/f4bf4e0097853438eb69c573bbaa0581e9b9c02d/modules/caddyhttp/reverseproxy/reverseproxy.go
func cloneRequest(origReq *http.Request) *http.Request {
	// Just clone the request - the reverse proxy will do the rewriting later in ServeHTTP
	req := new(http.Request)
	*req = *origReq
	if origReq.URL != nil {
		newURL := new(url.URL)
		*newURL = *origReq.URL
		if origReq.URL.User != nil {
			newURL.User = new(url.Userinfo)
			*newURL.User = *origReq.URL.User
		}
		req.URL = newURL
	}
	if origReq.Header != nil {
		req.Header = origReq.Header.Clone()
	}
	if origReq.Trailer != nil {
		req.Trailer = origReq.Trailer.Clone()
	}
	return req
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
	SamplingRate       float64         `json:"sampling_rate,omitempty"`
	RequestConcurrency int             `json:"request_concurrency,omitempty"`
	MaxBacklog         int             `json:"max_backlog,omitempty"`
	TargetServer       json.RawMessage `json:"target,omitempty"`
	RequestTimeout     time.Duration   `json:"request_timeout,omitempty"`

	requestChan     chan *http.Request
	cancelChan      chan interface{}
	rng             *rand.Rand
	logger          *zap.Logger
	droppedRequests atomic.Uint64
	reverseProxy    caddyhttp.MiddlewareHandler
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
	m.requestChan = make(chan *http.Request, m.MaxBacklog)
	m.cancelChan = make(chan interface{}, 1)
	m.rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))

	// Load the reverse proxy
	if m.TargetServer != nil {
		mod, err := ctx.LoadModuleByID("http.handlers.reverse_proxy", m.TargetServer)
		if err != nil {
			return fmt.Errorf("loading reverse proxy module failed: %w", err)
		}
		m.reverseProxy = mod.(caddyhttp.MiddlewareHandler)
	}

	for i := 0; i < m.RequestConcurrency; i++ {
		go m.mirrorWorker()
	}

	return nil
}

func (m *Mirror) Validate() error {
	if m.reverseProxy == nil {
		return fmt.Errorf("target is required")
	}

	return nil
}

func (m *Mirror) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	// As done in the forward_auth unmarshaler, use the following flow
	// Save our dispenser for the reverse_proxy unmarshaler
	// Consume all of our own directives, removing them from the dispenser
	// Call the reverse_proxy unmarshaler
	for d.Next() {
		for d.NextBlock(0) {
			// Only handle top-level directives
			if d.Nesting() != 1 {
				continue
			}

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
				d.DeleteN(2)
			case "concurrency":
				if !d.NextArg() {
					return d.ArgErr()
				}
				val, err := strconv.Atoi(d.Val())
				if err != nil {
					return fmt.Errorf("invalid concurrency: %w", err)
				}
				m.RequestConcurrency = val
				d.DeleteN(2)
			case "backlog":
				if !d.NextArg() {
					return d.ArgErr()
				}
				val, err := strconv.Atoi(d.Val())
				if err != nil {
					return fmt.Errorf("invalid backlog: %w", err)
				}
				m.MaxBacklog = val
				d.DeleteN(2)
			case "timeout":
				if !d.NextArg() {
					return d.ArgErr()
				}
				val, err := time.ParseDuration(d.Val())
				if err != nil {
					return fmt.Errorf("invalid timeout: %w", err)
				}
				m.RequestTimeout = val
				d.DeleteN(2)
			}
		}
	}
	// Reset the dispenser and unmarshal the reverse proxy
	d.Reset()
	d.Next()
	reverseProxy, err := caddyfile.UnmarshalModule(d, "http.handlers.reverse_proxy")
	if err != nil {
		return err
	}
	m.reverseProxy = reverseProxy.(caddyhttp.MiddlewareHandler)

	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Mirror
	err := m.UnmarshalCaddyfile(h.Dispenser)
	if err != nil {
		return nil, fmt.Errorf("parsing caddyfile failed: %w", err)
	}
	reverseProxyJson, err := json.Marshal(m.reverseProxy)
	if err != nil {
		return nil, fmt.Errorf("marshalling reverse proxy failed: %w", err)
	}
	m.TargetServer = reverseProxyJson
	return &m, err
}

func (m *Mirror) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if m.rng.Float64() < m.SamplingRate {
		select {
		case m.requestChan <- cloneRequest(r):
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

type discardWriter struct{}

func (d discardWriter) Header() http.Header {
	return http.Header{}
}

func (d discardWriter) Write(bytes []byte) (int, error) {
	return len(bytes), nil
}

func (d discardWriter) WriteHeader(_ int) {
	return
}

func (m *Mirror) mirrorWorker() {
	for {
		select {
		case _, _ = <-m.cancelChan:
			return
		case req := <-m.requestChan:
			// Add our goroutine ID to the headers
			req.Header.Set("X-Goroutine-ID", strconv.FormatInt(goid.Get(), 10))
			// Pass the request to the reverse proxy, with a no-op response writer
			w := discardWriter{}
			ctx, requestCancel := context.WithTimeout(req.Context(), m.RequestTimeout)
			if err := m.reverseProxy.ServeHTTP(w, req.WithContext(ctx), caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				return nil
			})); err != nil {
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
