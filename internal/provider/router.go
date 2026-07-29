package provider

import (
	"context"
	"fmt"
)

// Router selects a provider for a request and manages capability-gated
// failover between providers.
//
// Design informed by:
//   - Envoy: retry budgets, error classification, capability-gated failover
//   - LiteLLM: model-name routing, fallback chains
//   - Rejected: LiteLLM's NotFoundError bypass (#21377), no-preflight
//     fallback (#31557, #27967), mutable fallback dict (#28251)
type Router struct {
	Providers []Provider
}

// NewRouter creates a Router over the given providers.
func NewRouter(providers ...Provider) *Router {
	return &Router{Providers: providers}
}

// Route selects the first provider that can serve the request, checking
// both capability compatibility and model resolution.
//
// Returns a ProviderError with RetryNone if no provider can serve the
// request (not a panic, not a hang).
func (r *Router) Route(ctx context.Context, req *Request) (Provider, error) {
	for _, p := range r.Providers {
		if !p.Capabilities().CanServe(req.RequiredCapabilities) {
			continue
		}
		if _, ok := p.ResolveModel(req.Model); !ok {
			continue
		}
		return p, nil
	}
	return nil, &ProviderError{
		Provider: "router",
		Code:     ErrBadRequest,
		Message: fmt.Sprintf("no provider can serve model %q with required capabilities",
			req.Model),
		Retryable: RetryNone,
	}
}

// Failover selects the next provider for a failed request, given the
// providers already tried and the error from the last attempt.
//
// Capability-gated: checks CanServe before failover, rejecting the
// LiteLLM #31557 (context-window mismatch) and #27967 (assistant-prefill
// mismatch) anti-patterns.
//
// Respects ProviderError.Retryable:
//   - RetryNone: no failover, return the original error.
//   - RetryImmediately / RetryAfterBackoff / RetryAfterDuration: failover
//     to next compatible provider not in tried.
//
// Returns the original error if no more providers are available.
func (r *Router) Failover(
	ctx context.Context,
	req *Request,
	tried []Provider,
	lastErr *ProviderError,
) (Provider, error) {
	if lastErr != nil && lastErr.Retryable == RetryNone {
		return nil, lastErr
	}

	triedSet := make(map[string]bool, len(tried))
	for _, p := range tried {
		triedSet[p.ID()] = true
	}

	for _, p := range r.Providers {
		if triedSet[p.ID()] {
			continue
		}
		if !p.Capabilities().CanServe(req.RequiredCapabilities) {
			continue
		}
		if _, ok := p.ResolveModel(req.Model); !ok {
			continue
		}
		return p, nil
	}

	// No more providers — return the original error.
	if lastErr == nil {
		lastErr = &ProviderError{
			Provider: "router",
			Code:     ErrGateway,
			Message:  "all providers exhausted",
			Retryable: RetryNone,
		}
	}
	return nil, lastErr
}

// Execute runs a request through the router with automatic failover.
// It tries providers in order, failovering on retryable errors, until
// one succeeds or all are exhausted. This is the convenience method
// the proxy server would call (post-spike wiring).
func (r *Router) Execute(ctx context.Context, req *Request) (*Response, error) {
	var tried []Provider
	for {
		p, err := r.Route(ctx, req)
		if err != nil {
			// First Route failed — try failover from tried providers.
			pe, ok := err.(*ProviderError)
			if !ok {
				return nil, err
			}
			p, err = r.Failover(ctx, req, tried, pe)
			if err != nil {
				return nil, err
			}
		}

		tried = append(tried, p)
		resp, err := p.ChatCompletions(ctx, req)
		if err == nil {
			return resp, nil
		}

		pe, ok := err.(*ProviderError)
		if !ok {
			return nil, err
		}
		if pe.Retryable == RetryNone {
			return nil, pe
		}

		// Try failover.
		next, ferr := r.Failover(ctx, req, tried, pe)
		if ferr != nil {
			return nil, pe
		}
		// Continue loop with the next provider.
		_ = next
		// Route will skip tried providers via Failover, but Route doesn't
		// know about tried. For the spike's Execute, we use a simpler
		// approach: directly call the next provider.
		tried = append(tried, next)
		resp2, err2 := next.ChatCompletions(ctx, req)
		if err2 == nil {
			return resp2, nil
		}
		pe2, ok2 := err2.(*ProviderError)
		if !ok2 || pe2.Retryable == RetryNone {
			return nil, err2
		}
		// For the spike, we only do one level of failover in Execute.
		// Deeper failover chains are post-spike.
		return nil, pe2
	}
}
