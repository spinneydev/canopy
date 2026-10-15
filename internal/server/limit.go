package server

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// limiter bounds the requests that run at once: pool slots in all, and a
// share of them per visitor, so that one address sending slow requests
// (blame on a big file, a far-off comparison) can't hold every slot for
// everyone else.
type limiter struct {
	pool      chan struct{}
	perClient int

	mu      sync.Mutex
	clients map[string]*clientSlots // only visitors with a request running or waiting
}

type clientSlots struct {
	slots chan struct{}
	refs  int // requests holding one of slots or waiting for it
}

// newLimiter allows n requests at once, and a quarter of them (at least 2)
// from one visitor.
func newLimiter(n int) *limiter {
	return &limiter{pool: make(chan struct{}, n), perClient: max(2, n/4), clients: map[string]*clientSlots{}}
}

// acquire takes a slot for r, waiting briefly when the visitor's share or
// the whole pool is in use.
func (l *limiter) acquire(r *http.Request) (release func(), ok bool) {
	ctx, cancel := context.WithTimeout(r.Context(), queueTimeout)
	defer cancel()
	client := clientOf(r)
	if client == "" {
		if !take(ctx, l.pool) {
			return nil, false
		}
		return func() { <-l.pool }, true
	}

	l.mu.Lock()
	c := l.clients[client]
	if c == nil {
		c = &clientSlots{slots: make(chan struct{}, l.perClient)}
		l.clients[client] = c
	}
	c.refs++
	l.mu.Unlock()
	done := func() {
		l.mu.Lock()
		if c.refs--; c.refs == 0 {
			delete(l.clients, client)
		}
		l.mu.Unlock()
	}

	if !take(ctx, c.slots) {
		done()
		return nil, false
	}
	if !take(ctx, l.pool) {
		<-c.slots
		done()
		return nil, false
	}
	return func() { <-l.pool; <-c.slots; done() }, true
}

func take(ctx context.Context, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// clientOf names the visitor who sent r, for the per-visitor share. A
// request from this machine or the local network is taken to come through
// a proxy (Caddy, and Anubis, as install.sh sets them up), which says who
// its visitor is in X-Real-IP or X-Forwarded-For; one that doesn't say is
// left out of the per-visitor share ("") and only counts towards the pool.
// Anyone else is their own address: a header can't make them someone else.
// IPv6 addresses count by their /64, which one machine usually has whole.
func clientOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if !nearby(peer) {
		return visitor(peer)
	}
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return visitor(ip)
	}
	// Each proxy adds the address it was reached from, so the visitor is
	// the last one that isn't another proxy; anything to the left of it
	// was written by the visitor and proves nothing.
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !nearby(ip) {
			return visitor(ip)
		}
	}
	return ""
}

// nearby reports whether ip is this machine or on the local network.
func nearby(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate()
}

func visitor(ip netip.Addr) string {
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}
