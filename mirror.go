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

func (f *Mirror) Provision(ctx caddy.Context) error {
	f.EnsureDefaults()
	f.logger = ctx.Logger()
	f.logger.Debug("Running Provision")
	f.requestChan = make(chan Request, f.MaxBacklog)
	f.logger.Debug("Created request channel", zap.Int("capacity", f.MaxBacklog))
	f.cancelChan = make(chan interface{}, 1)
	f.rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	for i := 0; i < f.RequestConcurrency; i++ {
		go f.mirror_worker()
	}

	return nil
}

func (f *Mirror) Validate() error {
	f.logger.Debug("Running Validate")
	if f.TargetServer == "" {
		return fmt.Errorf("target server is required")
	}
	var err error
	f.parsedTarget, err = url.Parse(f.TargetServer)
	if err != nil {
		return err
	}

	if f.parsedTarget.Scheme == "" {
		f.parsedTarget.Scheme = "http"
		f.logger.Warn("Defaulting to HTTP for mirror target")
	}
	if f.parsedTarget.Scheme != "http" && f.parsedTarget.Scheme != "https" {
		return fmt.Errorf("target scheme must be either http or https")
	}
	if f.parsedTarget.RawPath != "" {
		return fmt.Errorf("path is not supported for mirror targets")
	}
	if f.parsedTarget.RawQuery != "" {
		return fmt.Errorf("query is not supported for mirror targets")
	}
	targetStr := fmt.Sprintf("%#v", f.parsedTarget)
	f.logger.Debug("Final target", zap.String("target", targetStr))

	return nil
}

func (f *Mirror) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	if f.logger != nil {
		f.logger.Debug("Running UnmarshalCaddyfile")
	}
	// Consume the directive token
	d.Next()
	if !d.NextArg() {
		return fmt.Errorf("block configuration is not yet implemented")
	}
	f.TargetServer = d.Val()

	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Mirror
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return &m, err
}

func (f *Mirror) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	f.logger.Debug("Running ServeHTTP")
	if f.rng.Float64() < f.SamplingRate {
		select {
		case f.requestChan <- serializeRequest(r):
			f.logger.Debug("Successfully queued request")
		default:
			if f.droppedRequests.Add(1) == 1 {
				go func() {
					time.Sleep(time.Duration(DROP_ALERT_INTERVAL) * time.Second)
					f.logger.Warn("Dropped requests due to full backlog", zap.Uint64("count", f.droppedRequests.Swap(0)))
				}()
			}
		}
	}
	return next.ServeHTTP(w, r)
}

func (f *Mirror) Cleanup() error {
	f.logger.Debug("Running Cleanup")
	close(f.cancelChan)
	close(f.requestChan)

	return nil
}

func create_roundtripper() http.RoundTripper {
	return &http.Transport{
		MaxIdleConns:    1,
		MaxConnsPerHost: 1,
	}
}

func (f *Mirror) mirror_worker() {
	f.logger.Debug("Running mirror_worker")
	// Create a RoundTripper to service all requests for this worker
	transport := create_roundtripper()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { f.logger.Debug("Finished mirror_worker") }()

	for {
		select {
		case _, _ = <-f.cancelChan:
			return
		case serReq := <-f.requestChan:
			f.logger.Debug("Worker got a request")
			req := serReq.deserialize(f.parsedTarget)
			//reqStr := fmt.Sprintf("%#v", req)
			//f.logger.Debug("Deserialized request", zap.String("request", reqStr))
			ctx, requestCancel := context.WithTimeout(baseCtx, f.RequestTimeout)
			//ctx := baseCtx
			if resp, err := transport.RoundTrip(req.WithContext(ctx)); err == nil {
				// Copy the response body to the bitbucket
				if _, err = io.Copy(io.Discard, resp.Body); err != nil {
					f.logger.Error("Failed to read response", zap.Error(err))
				}
				if err = resp.Body.Close(); err != nil {
					f.logger.Error("Failed to close response body", zap.Error(err))
				}
			} else {
				f.logger.Error("Failed to mirror request", zap.Error(err))
			}
			requestCancel()
		}
	}
}

func (f *Mirror) EnsureDefaults() {
	f.SamplingRate = max(f.SamplingRate, 1.0)
	f.RequestConcurrency = max(max(f.RequestConcurrency, runtime.GOMAXPROCS(0)/2), 1)
	if f.MaxBacklog < 1 {
		f.MaxBacklog = f.RequestConcurrency * 128
	}
	if f.RequestTimeout < 1 {
		f.RequestTimeout = time.Second * 5
	}
}
