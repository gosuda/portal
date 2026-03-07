package sdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"golang.org/x/sync/errgroup"
)

// RunHTTP serves one handler on the relay listener and, when localAddr is set,
// on the provided local HTTP address for app-local access.
func RunHTTP(ctx context.Context, relayListener net.Listener, handler http.Handler, localAddr string) error {
	relaySrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: defaultRequestTimeout,
	}

	var localSrv *http.Server
	if strings.TrimSpace(localAddr) != "" {
		localSrv = &http.Server{
			Addr:              strings.TrimSpace(localAddr),
			Handler:           handler,
			ReadHeaderTimeout: defaultRequestTimeout,
		}
	}

	group, groupCtx := errgroup.WithContext(ctx)
	if localSrv != nil {
		group.Go(func() error {
			if err := localSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serve local http: %w", err)
			}
			return nil
		})
	}
	group.Go(func() error {
		if err := relaySrv.Serve(relayListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve relay http: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		<-groupCtx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPShutdownTimeout)
		defer cancel()

		var localErr error
		if localSrv != nil {
			localErr = localSrv.Shutdown(shutdownCtx)
			if errors.Is(localErr, http.ErrServerClosed) {
				localErr = nil
			}
		}

		relayErr := relaySrv.Shutdown(shutdownCtx)
		if errors.Is(relayErr, http.ErrServerClosed) {
			relayErr = nil
		}

		return errors.Join(localErr, relayErr)
	})

	return group.Wait()
}

// SplitCSV splits a comma-separated string, trimming whitespace and dropping
// empty entries.
func SplitCSV(raw string) []string {
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
