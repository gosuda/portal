package utils

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DialTargetViaHTTPProxy establishes a TCP tunnel to targetAddr through the provided HTTP proxy.
// targetAddr must be host:port. authHeader is optional Proxy-Authorization value.
func DialTargetViaHTTPProxy(ctx context.Context, proxyURL *url.URL, targetAddr, authHeader string, timeout time.Duration) (net.Conn, error) {
	if proxyURL == nil {
		return nil, errors.New("proxy url is required")
	}
	if strings.TrimSpace(targetAddr) == "" {
		return nil, errors.New("target address is required")
	}
	proxyAddr := strings.TrimSpace(proxyURL.Host)
	if proxyAddr == "" {
		return nil, errors.New("proxy host is required")
	}
	if !strings.Contains(proxyAddr, ":") {
		proxyAddr = net.JoinHostPort(proxyAddr, "80")
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dial proxy: %w", err)
	}
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\nUser-Agent: portal-tunnel\r\n", targetAddr, targetAddr)
	if authHeader != "" {
		request += "Proxy-Authorization: " + authHeader + "\r\n"
	}
	request += "\r\n"
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	if resp.Body != nil {
		resp.Body.Close()
	}
	if resp.StatusCode/100 != 2 {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	if hasDeadline {
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, nil
}
