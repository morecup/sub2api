package tlsfingerprint

import (
	"sync"

	utls "github.com/refraction-networking/utls"
)

// versionedClientSessionCache keeps the negotiated TLS version beside uTLS'
// opaque session state. rustls omits the legacy TLS 1.2 session_ticket
// extension when it offers a TLS 1.3 PSK, so the ClientHello builder needs this
// fact before uTLS loads the cached ticket.
type versionedClientSessionCache struct {
	inner    utls.ClientSessionCache
	mu       sync.RWMutex
	versions map[string]uint16
}

// NewVersionedLRUClientSessionCache returns a uTLS session cache that also
// remembers the negotiated version for rustls-compatible resumed hellos.
func NewVersionedLRUClientSessionCache(capacity int) utls.ClientSessionCache {
	return &versionedClientSessionCache{
		inner:    utls.NewLRUClientSessionCache(capacity),
		versions: make(map[string]uint16),
	}
}

func (c *versionedClientSessionCache) Get(key string) (*utls.ClientSessionState, bool) {
	return c.inner.Get(key)
}

func (c *versionedClientSessionCache) Put(key string, state *utls.ClientSessionState) {
	c.inner.Put(key, state)
	if state == nil {
		c.mu.Lock()
		delete(c.versions, key)
		c.mu.Unlock()
	}
}

func (c *versionedClientSessionCache) recordNegotiatedVersion(key string, version uint16) {
	if key == "" || version == 0 {
		return
	}
	c.mu.Lock()
	c.versions[key] = version
	c.mu.Unlock()
}

func (c *versionedClientSessionCache) cachedVersion(key string) (uint16, bool) {
	if _, ok := c.inner.Get(key); !ok {
		return 0, false
	}
	c.mu.RLock()
	version, ok := c.versions[key]
	c.mu.RUnlock()
	return version, ok
}

type negotiatedVersionSessionCache interface {
	utls.ClientSessionCache
	recordNegotiatedVersion(key string, version uint16)
	cachedVersion(key string) (uint16, bool)
}
