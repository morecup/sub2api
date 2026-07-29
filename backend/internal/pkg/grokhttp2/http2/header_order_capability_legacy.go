//go:build !(go1.27 && !http2legacy)

package http2

// SupportsHeaderOrder reports whether this build of the Grok HTTP/2 fork can
// honor Transport.HeaderOrder at the real request-encoding seam.
func SupportsHeaderOrder() bool {
	return true
}
