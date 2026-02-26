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

	"gosuda.org/portal/portal/utils/pool"
	"gosuda.org/portal/sdk"
)

type Config struct {
	_ struct{} `version:"0.0.1" command:"portal-tunnel" about:"Expose local services through Portal relay"`

	RelayURL string `flag:"relay" env:"RELAY_URL" default:"http://localhost:4017" about:"Portal relay server URL"`
	Host     string `flag:"host" env:"APP_HOST" about:"Target host to proxy to (host:port or URL)"`
	Name     string `flag:"name" env:"APP_NAME" about:"Service name"`

	// Metadata.
	Description string `flag:"description" env:"APP_DESCRIPTION" about:"Service description metadata"`
	Tags        string `flag:"tags" env:"APP_TAGS" about:"Service tags metadata (comma-separated)"`
	Thumbnail   string `flag:"thumbnail" env:"APP_THUMBNAIL" about:"Service thumbnail URL metadata"`
	Owner       string `flag:"owner" env:"APP_OWNER" about:"Service owner metadata"`
	Hide        bool   `flag:"hide" env:"APP_HIDE" about:"Hide service from discovery (metadata)"`

	// Funnel workers.
	FunnelWorkers int `flag:"funnel-workers" env:"FUNNEL_WORKERS" default:"4" about:"Number of reverse connection workers"`
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		<-sigCh
		log.Info().Msg("Shutting down tunnel...")
		cancel()
	}()

	if err := runFunnelTunnel(ctx, cfg); err != nil {
		log.Error().Err(err).Msg("Exited with error")
		os.Exit(1)
	}

	log.Info().Msg("Tunnel stopped")
}

func runFunnelTunnel(ctx context.Context, cfg Config) error {
	// Normalize relay URL to HTTP base.
	baseURL := strings.TrimSuffix(cfg.RelayURL, "/relay")
	baseURL = strings.Replace(baseURL, "ws://", "http://", 1)
	baseURL = strings.Replace(baseURL, "wss://", "https://", 1)

	log.Info().Str("service", cfg.Name).Msg("Starting Portal Tunnel...")
	log.Info().Str("service", cfg.Name).Msgf("  Local:  %s", cfg.Host)
	log.Info().Str("service", cfg.Name).Msgf("  Relay:  %s", baseURL)

	client := sdk.NewFunnelClient(baseURL)

	var funnelOpts []sdk.FunnelOption
	if cfg.Description != "" {
		funnelOpts = append(funnelOpts, sdk.WithFunnelDescription(cfg.Description))
	}
	if cfg.Tags != "" {
		funnelOpts = append(funnelOpts, sdk.WithFunnelTags(strings.Split(cfg.Tags, ",")...))
	}
	if cfg.Thumbnail != "" {
		funnelOpts = append(funnelOpts, sdk.WithFunnelThumbnail(cfg.Thumbnail))
	}
	if cfg.Owner != "" {
		funnelOpts = append(funnelOpts, sdk.WithFunnelOwner(cfg.Owner))
	}
	if cfg.Hide {
		funnelOpts = append(funnelOpts, sdk.WithFunnelHide(cfg.Hide))
	}
	if cfg.FunnelWorkers > 0 {
		funnelOpts = append(funnelOpts, sdk.WithFunnelWorkers(cfg.FunnelWorkers))
	}

	listener, err := client.Register(cfg.Name, funnelOpts...)
	if err != nil {
		return fmt.Errorf("service %s: registration failed: %w", cfg.Name, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Info().Str("service", cfg.Name).Msg("")
	log.Info().Str("service", cfg.Name).Msg("Access via:")
	log.Info().Str("service", cfg.Name).Msgf("- HTTPS: %s", listener.PublicURL())
	log.Info().Str("service", cfg.Name).Msg("")

	connCount := 0
	var connWG sync.WaitGroup
	defer connWG.Wait()

	var relayConn net.Conn
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		relayConn, err = listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Error().Str("service", cfg.Name).Err(err).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		log.Info().Str("service", cfg.Name).Msgf("→ [#%d] New connection", connCount)

		connWG.Add(1)
		go func(rc net.Conn) {
			defer connWG.Done()
			if proxyErr := proxyConnection(ctx, cfg.Host, rc); proxyErr != nil {
				log.Error().Err(proxyErr).Str("service", cfg.Name).Msg("Proxy error")
			}
			log.Info().Str("service", cfg.Name).Msg("Connection closed")
		}(relayConn)
	}
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
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
			_ = relayConn.Close()
			_ = localConn.Close()
		case <-stopCh:
		}
	}()

	go func() {
		buf := *pool.Buffer64K.Get().(*[]byte)
		defer pool.Buffer64K.Put(&buf)
		_, copyErr := io.CopyBuffer(localConn, relayConn, buf)
		errCh <- copyErr
	}()

	go func() {
		buf := *pool.Buffer64K.Get().(*[]byte)
		defer pool.Buffer64K.Put(&buf)
		_, copyErr := io.CopyBuffer(relayConn, localConn, buf)
		errCh <- copyErr
	}()

	err = <-errCh
	close(stopCh)
	_ = relayConn.Close()
	<-errCh

	return err
}
