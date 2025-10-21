package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "relayserver",
	Short: "A lightweight, DNS-driven peer-to-peer proxy layer built on libp2p",
	RunE:  runServer,
}

var (
	flagP2pPort  int // libp2p outbound TCP/UDP port (e.g. 4001)
	flagHttpPort int // admin UI + HTTP proxy port (e.g. 8080)
	flagTcpPort  int // optional raw TCP ingress port (0 to disable)
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.IntVar(&flagHttpPort, "http-port", 8080, "admin UI and HTTP proxy port")
	flags.IntVar(&flagP2pPort, "p2p-port", 4001, "libp2p outbound TCP/UDP port")
	flags.IntVar(&flagTcpPort, "tcp-port", 0, "optional raw TCP ingress port (0 to disable)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := relaydns.MakeHost(ctx, flagP2pPort, true)
	if err != nil {
		return err
	}
	d, err := relaydns.NewRelayServer(ctx, h, relaydns.DefaultProtocol, relaydns.DefaultTopic)
	if err != nil {
		return err
	}

	// Admin UI + per-peer HTTP proxy served here
	httpServer := serveHTTP(ctx, fmt.Sprintf(":%d", flagHttpPort), d, h, cancel)

	// Optional raw TCP ingress (e.g., SSH)
	if flagTcpPort > 0 {
		go serveTCPIngress(ctx, fmt.Sprintf(":%d", flagTcpPort), d)
	}

	// graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("[server] shutting down...")

	// Cancel context to stop all goroutines
	cancel()

	// Shutdown HTTP server with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("[server] http server shutdown error")
	}

	// Close relay server (waits for background goroutines)
	if err := d.Close(); err != nil {
		log.Warn().Err(err).Msg("[server] relay server close error")
	}

	// Close libp2p host
	if err := h.Close(); err != nil {
		log.Warn().Err(err).Msg("[server] libp2p host close error")
	}

	log.Info().Msg("[server] shutdown complete")
	return nil
}

// serveTCPIngress listens on addr for raw TCP (e.g., SSH) and proxies
// incoming connections to a chosen peer over libp2p stream using Director.
func serveTCPIngress(ctx context.Context, addr string, d *relaydns.RelayServer) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error().Err(err).Msgf("tcp ingress listen failed: %s", addr)
		return
	}
	log.Info().Msgf("[server] tcp ingress: %s", addr)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			continue
		}
		go func(c net.Conn) {
			hosts := d.Hosts()
			if len(hosts) == 0 {
				log.Warn().Msg("tcp ingress: no backend peers available")
				_ = c.Close()
				return
			}
			// pick most recent (Hosts() sorted by last seen)
			peerID := hosts[0].Info.Peer
			if err := d.ProxyTCP(c, peerID); err != nil {
				log.Warn().Err(err).Msgf("tcp ingress proxy failed to %s", peerID)
			}
		}(conn)
	}
}
