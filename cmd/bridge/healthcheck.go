package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// healthcheckPingTimeout bounds each ping so a slow remote can't block the loop.
const healthcheckPingTimeout = 5 * time.Second

// runHealthcheckLoop pings HealthcheckURL on start then every HealthcheckInterval; failures are warn-logged (silence = down).
func (r *runner) runHealthcheckLoop(ctx context.Context) {
	if r.cfg.Source.HealthcheckURL == "" {
		return
	}
	r.log.Info("healthcheck started",
		"url", redactURL(r.cfg.Source.HealthcheckURL),
		"interval", r.cfg.Source.HealthcheckInterval)

	r.pingHealthcheck(ctx)

	ticker := time.NewTicker(r.cfg.Source.HealthcheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.pingHealthcheck(ctx)
		}
	}
}

// pingHealthcheck fires one GET with a 5s timeout; non-2xx and transport errors are logged but not propagated (silence = signal).
func (r *runner) pingHealthcheck(parent context.Context) {
	if r.cfg.Source.HealthcheckURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, healthcheckPingTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.Source.HealthcheckURL, nil)
	if err != nil {
		r.log.Warn("healthcheck request build failed", "err", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.log.Warn("healthcheck ping failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.log.Warn("healthcheck ping non-2xx", "status", resp.StatusCode)
		return
	}
	r.log.Debug("healthcheck ping ok")
}

// redactURL keeps the host visible but hides the path so logs don't leak the hc-ping.com UUID (the only auth).
func redactURL(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return "(set)"
	}
	rest := u[i+3:]
	j := strings.Index(rest, "/")
	if j < 0 {
		return u
	}
	return u[:i+3] + rest[:j] + "/…"
}
