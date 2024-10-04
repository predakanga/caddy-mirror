package mirror

import (
	"fmt"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"strconv"
	"time"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("mirror", parseCaddyfile)
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
		case "alert_interval":
			if !d.NextArg() {
				return d.ArgErr()
			}
			val, err := time.ParseDuration(d.Val())
			if err != nil {
				return fmt.Errorf("invalid alert interval: %w", err)
			}
			m.AlertInterval = val
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
