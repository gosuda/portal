package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"gosuda.org/portal/sdk"
)

//go:embed static
var staticFiles embed.FS

var rootCmd = &cobra.Command{
	Use:   "demo app",
	Short: "demo app using portal relay",
	RunE:  runPaint,
}

var (
	flagServerURL string
	flagPort      int
	flagName      string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&flagServerURL, "server-url", "ws://localhost:4017/relay", "relay websocket URL")
	flags.IntVar(&flagPort, "port", 8092, "local paint HTTP port")
	flags.StringVar(&flagName, "name", "demo-app", "backend display name")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute paint command")
	}
}

// DrawMessage represents a drawing action
type DrawMessage struct {
	Type   string  `json:"type"` // "draw", "shape", "text", or "clear"
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	PrevX  float64 `json:"prevX,omitempty"`
	PrevY  float64 `json:"prevY,omitempty"`
	StartX float64 `json:"startX,omitempty"`
	StartY float64 `json:"startY,omitempty"`
	EndX   float64 `json:"endX,omitempty"`
	EndY   float64 `json:"endY,omitempty"`
	Mode   string  `json:"mode,omitempty"` // "line", "circle", "rectangle"
	Text   string  `json:"text,omitempty"` // for text type
	Color  string  `json:"color,omitempty"`
	Width  int     `json:"width,omitempty"`
	Canvas string  `json:"canvas,omitempty"` // for initial state
}

// Canvas holds the current drawing state
type Canvas struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
	wg      sync.WaitGroup
	history []DrawMessage
}

func newCanvas() *Canvas {
	return &Canvas{
		clients: make(map[*websocket.Conn]bool),
		history: make([]DrawMessage, 0),
	}
}

func (c *Canvas) register(conn *websocket.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clients[conn] = true

	// Send history to new client
	for _, msg := range c.history {
		conn.WriteJSON(msg)
	}
}

func (c *Canvas) unregister(conn *websocket.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.clients[conn]; ok {
		delete(c.clients, conn)
		conn.Close()
	}
}

func (c *Canvas) broadcast(msg DrawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store in history
	switch msg.Type {
	case "draw", "shape", "text":
		c.history = append(c.history, msg)
	case "clear":
		c.history = make([]DrawMessage, 0)
	}

	// Broadcast to all clients
	for client := range c.clients {
		err := client.WriteJSON(msg)
		if err != nil {
			log.Error().Err(err).Msg("write to client")
			client.Close()
			delete(c.clients, client)
		}
	}
}

func (c *Canvas) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for client := range c.clients {
		client.Close()
	}
	c.clients = make(map[*websocket.Conn]bool)
}

func (c *Canvas) wait() {
	c.wg.Wait()
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (c *Canvas) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("upgrade websocket")
		return
	}

	c.register(conn)
	c.wg.Add(1)

	defer func() {
		c.unregister(conn)
		c.wg.Done()
	}()

	for {
		var msg DrawMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error().Err(err).Msg("read message")
			}
			break
		}
		c.broadcast(msg)
	}
}

func runPaint(cmd *cobra.Command, args []string) error {
	// 1) Create credential for this paint app
	cred := sdk.NewCredential()

	// 2) Create SDK client and connect to relay(s)
	client, err := sdk.NewClient(func(c *sdk.RDClientConfig) {
		c.BootstrapServers = []string{flagServerURL}
	})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer client.Close()

	// 3) Register lease and obtain a net.Listener that accepts relayed connections
	listener, err := client.Listen(cred, flagName, []string{"http/1.1"})
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	// 4) Setup HTTP handler
	canvas := newCanvas()
	mux := http.NewServeMux()

	// Serve static files from embedded filesystem
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("create static fs: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/ws", canvas.handleWS)

	// 5) Serve HTTP over relay listener
	log.Info().Msgf("[paint] serving HTTP over relay; lease=%s id=%s", flagName, cred.ID())

	srvErr := make(chan error, 1)
	go func() {
		srvErr <- http.Serve(listener, mux)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		log.Info().Msg("[paint] shutting down...")
	case err := <-srvErr:
		if err != nil {
			log.Error().Err(err).Msg("[paint] http serve error")
		}
	}

	canvas.closeAll()
	canvas.wait()

	log.Info().Msg("[paint] shutdown complete")
	return nil
}
