package mirror

import (
	"fmt"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime"
	"sync/atomic"
	"time"
)

func init() {
	caddy.RegisterModule(&Mirror{})
}

type Mirror struct {
	SamplingRate       float64       `json:"sampling_rate,omitempty"`
	RequestConcurrency int           `json:"request_concurrency,omitempty"`
	MaxBacklog         int           `json:"max_backlog,omitempty"`
	TargetServer       string        `json:"target,omitempty"`
	RequestTimeout     time.Duration `json:"request_timeout,omitempty"`
	AlertInterval      time.Duration `json:"alert_interval,omitempty"`

	requestChan     chan Request
	cancelChan      chan interface{}
	rng             *rand.Rand
	parsedTarget    *url.URL
	logger          *zap.Logger
	droppedRequests atomic.Uint64
}

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
		go m.mirrorWorker()
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

func (m *Mirror) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if m.rng.Float64() < m.SamplingRate {
		select {
		case m.requestChan <- serializeRequest(r):
		default:
			if m.droppedRequests.Add(1) == 1 {
				go func() {
					time.Sleep(m.AlertInterval)
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

func (m *Mirror) EnsureDefaults() {
	if m.SamplingRate <= 0 || m.SamplingRate >= 1.0 {
		m.SamplingRate = 1.0
	}
	// Default to half of maxprocs, and clamp to >= 1
	m.RequestConcurrency = max(max(m.RequestConcurrency, runtime.GOMAXPROCS(0)/2), 1)
	// Default to 128 requests per goroutine, clamp to >= 1
	if m.MaxBacklog < 1 {
		m.MaxBacklog = m.RequestConcurrency * 128
	}
	if m.RequestTimeout <= 0 {
		m.RequestTimeout = time.Second * 5
	}
	if m.AlertInterval <= 0 {
		m.AlertInterval = time.Second * 5
	}
}

var (
	_ caddy.Provisioner           = (*Mirror)(nil)
	_ caddy.Validator             = (*Mirror)(nil)
	_ caddyfile.Unmarshaler       = (*Mirror)(nil)
	_ caddyhttp.MiddlewareHandler = (*Mirror)(nil)
	_ caddy.CleanerUpper          = (*Mirror)(nil)
)
