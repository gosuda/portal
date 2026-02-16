package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/rs/zerolog/log"
	"gopkg.eu.org/broccoli"

	"gosuda.org/portal/sdk"
	utils "gosuda.org/portal/utils"
)

// bufferPool provides reusable 64KB buffers for io.CopyBuffer to eliminate
// per-copy allocations and reduce GC pressure under high concurrency.
// Using *[]byte to avoid interface boxing allocation in sync.Pool.
func newBufferPool() *sync.Pool {
	return &sync.Pool{
		New: func() any {
			b := make([]byte, 64*1024)
			return &b
		},
	}
}

type Config struct {
	_ struct{} `version:"0.0.1" command:"portal-tunnel" about:"Expose local services through Portal relay"`

	RelayURLs string `flag:"relay" env:"RELAYS" default:"https://localhost:4017/relay" about:"Portal relay server URLs (comma-separated)"`
	Host      string `flag:"host" env:"APP_HOST" about:"Target host to proxy to (host:port or URL)"`
	Name      string `flag:"name" env:"APP_NAME" about:"Service name"`

	// Metadata
	Protocols   string `flag:"protocols" env:"APP_PROTOCOLS" default:"http/1.1,h2" about:"ALPN protocols (comma-separated)"`
	Description string `flag:"description" env:"APP_DESCRIPTION" about:"Service description metadata"`
	Tags        string `flag:"tags" env:"APP_TAGS" about:"Service tags metadata (comma-separated)"`
	Thumbnail   string `flag:"thumbnail" env:"APP_THUMBNAIL" about:"Service thumbnail URL metadata"`
	Owner       string `flag:"owner" env:"APP_OWNER" about:"Service owner metadata"`
	Hide        bool   `flag:"hide" env:"APP_HIDE" about:"Hide service from discovery (metadata)"`
}

func main() {
	var cfg Config
	app, err := broccoli.NewApp(&cfg)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create app")
		os.Exit(1)
	}

	if _, _, err = app.Bind(&cfg, os.Args[1:]); err != nil {
		if errors.Is(err, broccoli.ErrHelp) {
			fmt.Println(app.Help())
			os.Exit(0)
		}

		fmt.Println(app.Help())
		log.Error().Err(err).Msg("Failed to bind CLI arguments")
		os.Exit(1)
	}

	if cfg.Host == "" || cfg.Name == "" {
		fmt.Println(app.Help())
		os.Exit(1)
	}

	relayURLs := utils.ParseURLs(cfg.RelayURLs)
	if len(relayURLs) == 0 {
		log.Error().Msg("--relay must include at least one non-empty URL")
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info().Msg("Shutting down tunnel...")
		cancel()
	}()

	bufferPool := newBufferPool()
	runErr := runServiceTunnel(ctx, relayURLs, cfg, "flags", bufferPool)
	signal.Stop(sigCh)
	cancel()
	if runErr != nil {
		log.Error().Err(runErr).Msg("Exited with error")
		os.Exit(1)
	}

	log.Info().Msg("Tunnel stopped")
}

func runServiceTunnel(ctx context.Context, relayURLs []string, cfg Config, origin string, bufferPool *sync.Pool) error {
	if len(relayURLs) == 0 {
		return errors.New("no relay URLs provided")
	}
	protocols := strings.Split(cfg.Protocols, ",")
	if len(protocols) == 0 {
		protocols = []string{"http/1.1", "h2"}
	}

	cred := sdk.NewCredential()
	leaseID := cred.ID()
	if cfg.Name == "" {
		cfg.Name = "tunnel-" + leaseID[:8]
		log.Info().Str("service", cfg.Name).Msg("No service name provided; generated automatically")
	}
	log.Info().Str("service", cfg.Name).Msgf("Local service is reachable at %s", cfg.Host)
	log.Info().Str("service", cfg.Name).Msgf("Starting Portal Tunnel (%s)...", origin)
	log.Info().Str("service", cfg.Name).Msgf("  Local:    %s", cfg.Host)
	log.Info().Str("service", cfg.Name).Msgf("  Relays:   %s", strings.Join(relayURLs, ", "))
	log.Info().Str("service", cfg.Name).Msgf("  Lease ID: %s", leaseID)

	client, err := sdk.NewClient(func(c *sdk.ClientConfig) {
		c.BootstrapServers = relayURLs
	})
	if err != nil {
		return fmt.Errorf("service %s: failed to connect to relay: %w", cfg.Name, err)
	}
	defer client.Close()

	listener, err := client.Listen(cred, cfg.Name, protocols,
		sdk.WithDescription(cfg.Description),
		sdk.WithTags(strings.Split(cfg.Tags, ",")),
		sdk.WithOwner(cfg.Owner),
		sdk.WithThumbnail(cfg.Thumbnail),
		sdk.WithHide(cfg.Hide),
	)
	if err != nil {
		return fmt.Errorf("service %s: failed to register service: %w", cfg.Name, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Info().Str("service", cfg.Name).Msg("")
	log.Info().Str("service", cfg.Name).Msg("Access via:")
	log.Info().Str("service", cfg.Name).Msgf("- Name:     /peer/%s", cfg.Name)
	log.Info().Str("service", cfg.Name).Msgf("- Lease ID: /peer/%s", leaseID)
	log.Info().Str("service", cfg.Name).Msgf("- Example:  %s/peer/%s", relayURLs[0], cfg.Name)

	log.Info().Str("service", cfg.Name).Msg("")

	connCount := 0
	var connWG sync.WaitGroup
	defer connWG.Wait()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		relayConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Error().Str("service", cfg.Name).Err(acceptErr).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		log.Info().Str("service", cfg.Name).Msgf("→ [#%d] New connection from %s", connCount, relayConn.RemoteAddr())

		connWG.Add(1)
		go func(relayConn net.Conn) {
			defer connWG.Done()
			proxyErr := proxyConnection(ctx, cfg.Host, relayConn, bufferPool)
			if proxyErr != nil {
				log.Error().Str("service", cfg.Name).Err(proxyErr).Msg("Proxy error")
			}
			log.Info().Str("service", cfg.Name).Msg("Connection closed")
		}(relayConn)
	}
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn, bufferPool *sync.Pool) error {
	defer relayConn.Close()

	dialer := new(net.Dialer)
	localConn, err := dialer.DialContext(ctx, "tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to local service %s: %w", localAddr, err)
	}
	defer localConn.Close()

	errCh := make(chan error, 2)
	stopCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			relayCloseErr := relayConn.Close()
			if relayCloseErr != nil && !errors.Is(relayCloseErr, net.ErrClosed) {
				log.Debug().Err(relayCloseErr).Msg("failed to close relay connection")
			}
			localCloseErr := localConn.Close()
			if localCloseErr != nil && !errors.Is(localCloseErr, net.ErrClosed) {
				log.Debug().Err(localCloseErr).Msg("failed to close local connection")
			}
		case <-stopCh:
		}
	}()

	go func() {
		buf := *bufferPool.Get().(*[]byte)
		defer bufferPool.Put(&buf)
		_, copyErr := io.CopyBuffer(localConn, relayConn, buf)
		errCh <- copyErr
	}()

	go func() {
		buf := *bufferPool.Get().(*[]byte)
		defer bufferPool.Put(&buf)
		_, copyErr := io.CopyBuffer(relayConn, localConn, buf)
		errCh <- copyErr
	}()

	err = <-errCh
	close(stopCh)
	relayCloseErr := relayConn.Close()
	if relayCloseErr != nil && !errors.Is(relayCloseErr, net.ErrClosed) {
		log.Debug().Err(relayCloseErr).Msg("failed to close relay connection")
	}
	<-errCh

	return err
}
