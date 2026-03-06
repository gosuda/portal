package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/sdk"
)

type relayRuntime struct {
	client   *sdk.Client
	listener *sdk.Listener
	relayURL string
}

type relayLoopResult struct {
	err      error
	leaseID  string
	relayURL string
}

func (r *relayRuntime) run(ctx context.Context, localAddr string, connWG *sync.WaitGroup, connCount *atomic.Int64, done chan<- relayLoopResult) {
	logger := log.With().
		Str("component", "portal-tunnel").
		Str("relay", r.relayURL).
		Str("lease_id", r.listener.LeaseID()).
		Logger()

	var runErr error
	defer func() {
		done <- relayLoopResult{
			leaseID:  r.listener.LeaseID(),
			relayURL: r.relayURL,
			err:      runErr,
		}
	}()

	for {
		relayConn, err := r.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			runErr = err
			return
		}

		connID := connCount.Add(1)
		logger.Info().
			Int64("conn_id", connID).
			Str("remote_addr", relayConn.RemoteAddr().String()).
			Msg("accepted relay connection")

		connWG.Add(1)
		go func(connID int64, relayConn net.Conn) {
			defer connWG.Done()
			if err := proxyConnection(ctx, localAddr, relayConn); err != nil {
				logger.Error().Err(err).Int64("conn_id", connID).Msg("proxy connection failed")
			}
			logger.Info().Int64("conn_id", connID).Msg("proxy connection closed")
		}(connID, relayConn)
	}
}

func startRelayRuntimes(ctx context.Context, relayURLs []string, req sdk.ListenRequest) ([]*relayRuntime, error) {
	runtimes := make([]*relayRuntime, 0, len(relayURLs))
	for _, relayURL := range relayURLs {
		client, err := sdk.NewClient(sdk.ClientConfig{RelayURL: relayURL})
		if err != nil {
			_ = closeRelayRuntimes(runtimes)
			return nil, fmt.Errorf("create relay client %s: %w", relayURL, err)
		}

		listener, err := client.Listen(ctx, req)
		if err != nil {
			client.Close()
			_ = closeRelayRuntimes(runtimes)
			return nil, fmt.Errorf("register relay lease %s: %w", relayURL, err)
		}

		runtimes = append(runtimes, &relayRuntime{
			relayURL: relayURL,
			client:   client,
			listener: listener,
		})
	}
	return runtimes, nil
}

func closeRelayRuntimes(runtimes []*relayRuntime) error {
	var closeErr error
	for _, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		if runtime.listener != nil {
			closeErr = errors.Join(closeErr, runtime.listener.Close())
		}
		if runtime.client != nil {
			runtime.client.Close()
		}
	}
	return closeErr
}

func waitForRelayLoops(ctx context.Context, done <-chan relayLoopResult, relayCount int) error {
	logger := log.With().Str("component", "portal-tunnel").Logger()
	active := relayCount

	for active > 0 {
		result := <-done
		active--

		switch {
		case result.err != nil:
			logger.Error().
				Err(result.err).
				Str("relay", result.relayURL).
				Str("lease_id", result.leaseID).
				Int("remaining_relays", active).
				Msg("relay accept loop stopped")
		case ctx.Err() != nil:
			logger.Info().
				Str("relay", result.relayURL).
				Str("lease_id", result.leaseID).
				Int("remaining_relays", active).
				Msg("relay accept loop stopped during shutdown")
		default:
			logger.Warn().
				Str("relay", result.relayURL).
				Str("lease_id", result.leaseID).
				Int("remaining_relays", active).
				Msg("relay accept loop stopped")
		}
	}

	select {
	case <-ctx.Done():
		return nil
	default:
	}
	return errors.New("all relay listeners stopped")
}

func normalizeRelayURLs(raw string) ([]string, error) {
	seen := make(map[string]struct{})
	var relayURLs []string
	for _, relayURL := range splitCSV(raw) {
		normalized, err := normalizeRelayURL(relayURL)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		relayURLs = append(relayURLs, normalized)
	}
	return relayURLs, nil
}

func normalizeRelayURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse relay url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("relay url must use https: %q", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("relay url host is empty: %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	targetAddr, err := normalizeTargetAddr(localAddr)
	if err != nil {
		return fmt.Errorf("invalid --host value %q: %w", localAddr, err)
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	localConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		return writeEmptyHTTPResponse(relayConn)
	}
	defer localConn.Close()

	errCh := make(chan error, 2)
	stopCh := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			_ = relayConn.Close()
			_ = localConn.Close()
		case <-stopCh:
		}
	}()

	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(localConn, relayConn, *bufPtr)
		if tcpConn, ok := localConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		errCh <- err
	}()

	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(relayConn, localConn, *bufPtr)
		_ = relayConn.Close()
		errCh <- err
	}()

	var firstErr error
	for range 2 {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}

	close(stopCh)
	if errors.Is(firstErr, io.EOF) || errors.Is(firstErr, net.ErrClosed) {
		return nil
	}
	return firstErr
}

func writeEmptyHTTPResponse(conn net.Conn) error {
	htmlBody := `<!DOCTYPE html>
<html>
<head><title>Service Unavailable</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px;">
<h1>Service Unavailable</h1>
<p>The local service is not currently running.</p>
<p>Please start your local application and refresh this page.</p>
</body>
</html>`
	response := fmt.Sprintf("HTTP/1.1 503 Service Unavailable\r\n"+
		"Content-Type: text/html; charset=utf-8\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: close\r\n"+
		"\r\n%s", len(htmlBody), htmlBody)
	_, err := conn.Write([]byte(response))
	return err
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeTargetAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("target address is required")
	}
	if strings.Contains(raw, "://") {
		if strings.HasPrefix(strings.ToLower(raw), "http://") {
			raw = strings.TrimPrefix(raw, "http://")
		}
		if strings.HasPrefix(strings.ToLower(raw), "https://") {
			raw = strings.TrimPrefix(raw, "https://")
		}
		raw = strings.TrimSuffix(raw, "/")
	}
	if _, _, err := net.SplitHostPort(raw); err == nil {
		return raw, nil
	}
	if strings.Count(raw, ":") == 0 {
		return net.JoinHostPort(raw, "80"), nil
	}
	return "", fmt.Errorf("invalid target address %q", raw)
}
