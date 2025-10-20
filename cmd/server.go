package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gosuda/relaydns/pkg"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "relayserver",
	Short: "A lightweight, DNS-driven peer-to-peer proxy layer built on libp2p",
	RunE:  runServer,
}

var (
	flagBootstraps []string
	flagRelay      bool
	listenTCP      string
	listenHTTP     string
	protocol       string
	topic          string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")
	flags.BoolVar(&flagRelay, "relay", true, "enable libp2p relay support")
	flags.StringVar(&listenTCP, "listen-tcp", ":22", "TCP listen (e.g. :22 for SSH)")
	flags.StringVar(&listenHTTP, "listen-http", ":8080", "HTTP admin API")
	flags.StringVar(&protocol, "protocol", "/relaydns/ssh/1.0", "libp2p protocol id for streams")
	flags.StringVar(&topic, "topic", "relaydns.backends", "pubsub topic for adverts")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := pkg.MakeHost(ctx, flagRelay)
	if err != nil {
		return err
	}
	pkg.ConnectBootstraps(ctx, h, flagBootstraps)

	d, err := pkg.NewDirector(ctx, h, protocol, topic)
	if err != nil {
		return err
	}

	go func() {
		if err := d.ServeHTTP(listenHTTP); err != nil {
			log.Println("http api:", err)
			cancel()
		}
	}()
	go func() {
		if err := d.ServeTCP(listenTCP); err != nil {
			log.Println("tcp:", err)
			cancel()
		}
	}()

	// graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	time.Sleep(300 * time.Millisecond)
	return nil
}
