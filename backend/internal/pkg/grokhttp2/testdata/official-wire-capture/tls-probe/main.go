package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type request struct {
	Address  string `json:"address"`
	ProxyURL string `json:"proxy_url"`
}

type response struct {
	OK bool `json:"ok"`
}

var h2Preamble = []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00")

func main() {
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(os.Stdin, 64<<10)))
	var input request
	if err := decoder.Decode(&input); err != nil {
		fatal("decode request", err)
	}
	if input.Address == "" || input.ProxyURL == "" {
		fatal("validate request", fmt.Errorf("address and proxy_url are required"))
	}
	proxyURL, err := url.Parse(input.ProxyURL)
	if err != nil {
		fatal("parse proxy URL", err)
	}

	cache := tlsfingerprint.NewVersionedLRUClientSessionCache(256)
	profile := tlsfingerprint.GrokCLIProfile().WithSessionCache(cache)
	dialer := tlsfingerprint.NewHTTPProxyDialer(profile, proxyURL)
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		conn, dialErr := dialer.DialTLSContext(ctx, "tcp", input.Address)
		cancel()
		if dialErr != nil {
			fatal("dial TLS", dialErr)
		}
		if err := exerciseH2Connection(conn); err != nil {
			_ = conn.Close()
			fatal("exercise HTTP/2 connection", err)
		}
		_ = conn.Close()
	}
	_ = json.NewEncoder(os.Stdout).Encode(response{OK: true})
}

func exerciseH2Connection(conn interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	SetDeadline(time.Time) error
	Close() error
}) error {
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write(h2Preamble); err != nil {
		return err
	}

	// Reading until an idle deadline gives uTLS a chance to consume the TLS 1.3
	// NewSessionTicket that follows the handshake. Post-handshake records are
	// handled inside uTLS and are never exposed to this helper.
	buffer := make([]byte, 32<<10)
	sawApplicationData := false
	for {
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		n, err := conn.Read(buffer)
		if n > 0 {
			sawApplicationData = true
		}
		if err != nil {
			if sawApplicationData {
				return nil
			}
			return err
		}
	}
}

func fatal(operation string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
	os.Exit(1)
}
