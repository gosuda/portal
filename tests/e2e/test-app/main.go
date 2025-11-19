package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gosuda.org/portal/sdk"
)

var (
	flagRelay string
	flagPort  int
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	flag.StringVar(&flagRelay, "relay", "ws://localhost:4017/relay", "Relay server URL")
	flag.IntVar(&flagPort, "port", 3000, "Local HTTP port")
	flag.Parse()

	// 1. Start local HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			<!DOCTYPE html>
			<html>
			<head><title>E2E Test App</title></head>
			<body>
				<h1>Hello World</h1>
				<div id="status">Connected</div>
			</body>
			</html>
		`))
	})

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Error().Err(err).Msg("upgrade")
			return
		}
		defer c.Close()
		log.Info().Msg("Client connected")
		for {
			mt, message, err := c.ReadMessage()
			if err != nil {
				log.Info().Msg("Client disconnected")
				break
			}
			log.Info().Str("msg", string(message)).Msg("Received")
			err = c.WriteMessage(mt, message)
			if err != nil {
				log.Error().Err(err).Msg("write")
				break
			}
		}
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", flagPort),
		Handler: mux,
	}

	go func() {
		log.Info().Int("port", flagPort).Msg("Starting local HTTP server")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	// 2. Register with Portal using SDK
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cred := sdk.NewCredential()
	log.Info().Str("lease_id", cred.ID()).Msg("Generated credential")

	client, err := sdk.NewClient(sdk.WithBootstrapServers([]string{flagRelay}))
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create SDK client")
	}
	defer client.Close()

	// Register service "test-app"
	listener, err := client.Listen(cred, "test-app", []string{"http/1.1"})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to listen on Portal")
	}
	defer listener.Close()

	log.Info().Msg("Registered with Portal as 'test-app'")
	fmt.Printf("PORTAL_URL=http://test-app.localhost:4017\n") // Assuming default portal config

	// 3. Proxy connections
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					log.Error().Err(err).Msg("Accept failed")
					continue
				}
			}
			go func() {
				defer conn.Close()
				// Connect to local HTTP server
				localConn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", flagPort))
				if err != nil {
					log.Error().Err(err).Msg("Failed to connect to local server")
					return
				}
				defer localConn.Close()

				// Proxy
				errCh := make(chan error, 2)
				go func() {
					_, err := io.Copy(localConn, conn)
					errCh <- err
				}()
				go func() {
					_, err := io.Copy(conn, localConn)
					errCh <- err
				}()
				<-errCh
			}()
		}
	}()

	<-ctx.Done()
	log.Info().Msg("Shutting down...")
	server.Shutdown(context.Background())
}
