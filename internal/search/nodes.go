package search

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// A cluster is reachable through any of its nodes: each one routes requests to
// the shards wherever they live. The client spreads requests round-robin over
// the configured nodes and, when a node cannot be reached, retries on the next
// one, so losing a node does not fail requests (experiment 07).
//
// A node that fails is skipped for deadCooldown, so that requests do not each
// pay for a failed connection attempt (on a real network, a dead host costs a
// full dial timeout). After the cooldown it is tried again; if every node is
// marked dead they are all tried anyway, as a last resort.
const deadCooldown = 30 * time.Second

// dialTimeout bounds connection attempts to a node that is down or unreachable.
const dialTimeout = 2 * time.Second

type node struct {
	url       string
	deadUntil atomic.Int64 // unix nanoseconds; 0 = alive
}

type nodePool struct {
	nodes []*node
	next  atomic.Uint64
	now   func() time.Time
}

// parseNodes splits a comma-separated list of node URLs.
func parseNodes(urls string) []*node {
	var nodes []*node
	for _, u := range strings.Split(urls, ",") {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			nodes = append(nodes, &node{url: u})
		}
	}
	return nodes
}

// order returns the nodes to try for one request: live nodes first, starting
// at the next one in round-robin order, then nodes still in their cooldown.
func (p *nodePool) order() []*node {
	start := int(p.next.Add(1) % uint64(len(p.nodes)))
	now := p.now().UnixNano()
	live := make([]*node, 0, len(p.nodes))
	var dead []*node
	for i := range p.nodes {
		n := p.nodes[(start+i)%len(p.nodes)]
		if n.deadUntil.Load() > now {
			dead = append(dead, n)
		} else {
			live = append(live, n)
		}
	}
	return append(live, dead...)
}

func (p *nodePool) markDead(n *node, err error) {
	if n.deadUntil.Swap(p.now().Add(deadCooldown).UnixNano()) == 0 {
		slog.Warn("elasticsearch node unreachable; skipping it", "node", n.url, "for", deadCooldown, "err", err)
	}
}

func (p *nodePool) markAlive(n *node) {
	if n.deadUntil.Swap(0) != 0 {
		slog.Info("elasticsearch node reachable again", "node", n.url)
	}
}

// notSent reports whether a transport error happened before the request
// reached a node (DNS lookup or connection failure). Such a request can be
// sent to another node whatever it does.
func notSent(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// readOnly reports whether a request only reads, so it is safe to resend after
// a failure that may have happened once the node had received it.
func readOnly(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	p := path
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	for _, suffix := range []string{"/_search", "/_count", "/_rank_eval", "/_mget", "/_msearch"} {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

// retryableStatus reports responses meaning "this node cannot serve the request
// right now" (for example a node that is shutting down, or behind a proxy).
func retryableStatus(code int) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}
