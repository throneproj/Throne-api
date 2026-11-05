package fakethrone

import (
	"context"
	"fmt"
	"slices"
	"time"

	throne "github.com/throneproj/Throne-api"
)

const warpTerms = "https://www.cloudflare.com/application/terms/"

var warpModes = []string{throne.WarpWireGuard, throne.WarpAmnezia, throne.WarpMASQUE}

type warpState struct {
	enabled, termsAccepted bool
	mode                   string
	// wireguard serves the amnezia mode too, which also needs its packets.
	wireguard, masque *warpDevice
	amneziaPackets    bool
	registering       *op
}

type warpDevice struct {
	endpoint  string
	addresses []string
}

func (w *warpState) configured(mode string) bool {
	switch mode {
	case throne.WarpMASQUE:
		return w.masque != nil
	case throne.WarpAmnezia:
		return w.wireguard != nil && w.amneziaPackets
	}
	return w.wireguard != nil
}

func (s *Server) warpView() throne.Warp {
	w := &s.warp
	v := throne.Warp{Enabled: w.enabled, Mode: w.mode, Configured: w.configured(w.mode), Addresses: []string{},
		TermsAccepted: w.termsAccepted, TermsURL: warpTerms, Registering: w.registering != nil}
	d := w.wireguard
	if w.mode == throne.WarpMASQUE {
		d = w.masque
	}
	if d != nil {
		v.Endpoint, v.Addresses = d.endpoint, slices.Clone(d.addresses)
	}
	return v
}

func (s *Server) getWarp(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok(s.warpView())
}

func (s *Server) putWarp(_ context.Context, r *request) *response {
	hasEnabled := r.fields["enabled"] != nil
	enabled, bad := boolField(r.fields, "enabled", false)
	if bad != nil {
		return bad
	}
	mode, hasMode, bad := stringField(r.fields, "mode")
	switch {
	case bad != nil:
		return bad
	case !hasEnabled && !hasMode:
		return badRequest("Give enabled, mode or both")
	case hasMode && !slices.Contains(warpModes, mode):
		return badRequest("mode must be wireguard, amnezia or masque")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasEnabled {
		enabled = s.warp.enabled
	}
	if !hasMode {
		mode = s.warp.mode
	}
	if enabled && !s.warp.configured(mode) {
		return fail(409, "warp_not_configured", "WARP has no registered device for the %s mode; register one first", mode)
	}
	if enabled != s.warp.enabled || mode != s.warp.mode {
		wasEnabled := s.warp.enabled
		s.warp.enabled, s.warp.mode = enabled, mode
		s.logf("[WARP] %s, mode %s", map[bool]string{true: "Enabled", false: "Disabled"}[enabled], mode)
		s.restartNeeded = s.restartNeeded || (enabled || wasEnabled) && s.state == "running"
		s.bump(throne.TopicStatus, throne.TopicSettings)
	}
	return ok(throne.WarpChange{Warp: s.warpView(), RestartNeeded: s.restartNeeded})
}

func (s *Server) registerWarp(ctx context.Context, r *request) *response {
	mode, hasMode, bad := stringField(r.fields, "mode")
	if bad != nil {
		return bad
	}
	if hasMode && !slices.Contains(warpModes, mode) {
		return badRequest("mode must be wireguard, amnezia or masque")
	}
	accept, bad := boolField(r.fields, "accept_terms", false)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasMode {
		mode = s.warp.mode
	}
	switch {
	case s.warp.registering != nil:
		return busy("A WARP device is being registered already")
	case !s.warp.termsAccepted && !accept:
		return fail(409, "terms_required", "Registering a WARP device needs acceptance of Cloudflare's WARP terms: %s", warpTerms)
	}
	if !s.warp.termsAccepted {
		s.warp.termsAccepted = true
		s.bump(throne.TopicSettings)
	}
	o := newOp()
	s.warp.registering = o
	s.logf("[WARP] Registering a device for the %s mode", mode)
	s.after(1500*time.Millisecond+time.Duration(s.rnd.IntN(1500))*time.Millisecond, func() {
		s.warp.registering = nil
		if s.rnd.IntN(6) == 0 {
			s.logf("%s [WARP] Registration failed: HTTP 429 Too Many Requests", level("WARN"))
			o.end(fail(502, "warp_failed", "Cloudflare refused the registration: HTTP 429 Too Many Requests"))
			return
		}
		v6 := fmt.Sprintf("2606:4700:110:%x:%x:%x:%x:%x/128", s.rnd.IntN(0x10000), s.rnd.IntN(0x10000),
			s.rnd.IntN(0x10000), s.rnd.IntN(0x10000), s.rnd.IntN(0x10000))
		switch mode {
		case throne.WarpMASQUE:
			s.warp.masque = &warpDevice{endpoint: "162.159.198.1:443", addresses: []string{"172.16.0.2/32", v6}}
		default:
			s.warp.wireguard = &warpDevice{endpoint: "engage.cloudflareclient.com:2408", addresses: []string{"172.16.0.2/32", v6}}
			s.warp.amneziaPackets = s.warp.amneziaPackets || mode == throne.WarpAmnezia
		}
		s.warp.mode = mode
		s.restartNeeded = s.restartNeeded || s.warp.enabled && s.state == "running"
		s.logf("[WARP] Registered a device for the %s mode", mode)
		s.bump(throne.TopicStatus, throne.TopicSettings)
		o.end(ok(throne.WarpChange{Warp: s.warpView(), RestartNeeded: s.restartNeeded}))
	})
	if !s.await(ctx, o, 90*time.Second) {
		if ctx.Err() != nil {
			return nil
		}
		return fail(502, "warp_failed", "Cloudflare did not answer within 90 seconds")
	}
	return o.resp
}
