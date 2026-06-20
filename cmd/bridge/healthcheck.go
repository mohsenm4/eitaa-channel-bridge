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
	r.pingHealthcheckURL(parent, r.cfg.Source.HealthcheckURL, "ok")
}

// recordPublishFailure bumps the consecutive-failure streak and pings /fail once the threshold is crossed.
func (r *runner) recordPublishFailure(ctx context.Context) {
	r.publishFailStreak++
	threshold := r.cfg.Source.HealthcheckFailureThreshold
	if threshold <= 0 || r.publishFailAlerted || r.publishFailStreak < threshold {
		return
	}
	r.log.Warn("healthcheck: flipping to DOWN after consecutive publish failures",
		"streak", r.publishFailStreak, "threshold", threshold)
	r.pingHealthcheckFail(ctx)
	r.publishFailAlerted = true
}

// recordPublishSuccess clears the failure streak so the next ping flips the check back to UP.
func (r *runner) recordPublishSuccess() {
	r.publishFailStreak = 0
	r.publishFailAlerted = false
}

// pingHealthcheckFail flips the dead-man's-switch to DOWN by appending /fail to the configured URL.
func (r *runner) pingHealthcheckFail(parent context.Context) {
	if r.cfg.Source.HealthcheckURL == "" {
		return
	}
	r.pingHealthcheckURL(parent, strings.TrimRight(r.cfg.Source.HealthcheckURL, "/")+"/fail", "fail")
}

// pingHealthcheckURL fires one GET with a short timeout; non-2xx and transport errors are logged but not propagated.
func (r *runner) pingHealthcheckURL(parent context.Context, url, kind string) {
	if url == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, healthcheckPingTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.log.Warn("healthcheck request build failed", "kind", kind, "err", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.log.Warn("healthcheck ping failed", "kind", kind, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.log.Warn("healthcheck ping non-2xx", "kind", kind, "status", resp.StatusCode)
		return
	}
	r.log.Debug("healthcheck ping ok", "kind", kind)
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
