package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall/js"
	"time"

	"github.com/gosuda/portal/cmd/webclient/httpjs"
	"github.com/gosuda/portal/sdk"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/idna"
)

var (
	bootstrapServers = []string{"ws://localhost:4017/relay", "wss://portal.gosuda.org/relay"}
	rdClient         *sdk.RDClient
)

var client = &http.Client{
	Timeout: time.Second * 30,
	Transport: &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			address = strings.TrimSuffix(address, ":80")
			address = strings.TrimSuffix(address, ":443")
			cred := sdk.NewCredential()
			conn, err := rdClient.Dial(cred, address, "http/1.1")
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
	},
}

type Proxy struct {
}

// IsHTMLContentType checks if the Content-Type header indicates HTML content
// It properly handles media type parsing with parameters like charset
func IsHTMLContentType(contentType string) bool {
	if contentType == "" {
		return false
	}

	// Parse the media type and parameters
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// If parsing fails, do a simple case-insensitive check for "text/html"
		return strings.HasPrefix(strings.ToLower(contentType), "text/html")
	}

	// Check if the media type is HTML
	return mediaType == "text/html"
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Info().Msgf("Proxying request to %s", r.URL.String())

	host, err := idna.ToUnicode(r.URL.Hostname())
	if err != nil {
		host = r.URL.Hostname()
	}
	id := strings.Split(host, ".")[0]
	id = strings.TrimSpace(id)
	id = strings.ToUpper(id)

	r = r.Clone(context.Background())
	r.URL.Host = id
	r.URL.Scheme = "http"

	resp, err := client.Do(r)
	if err != nil {
		log.Error().Err(err).Msgf("Failed to proxy request to %s", r.URL.String())
		http.Error(w, fmt.Sprintf("Failed to proxy request to %s, err: %v", r.URL.String(), err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, value := range resp.Header {
		w.Header()[key] = value
	}

	if IsHTMLContentType(resp.Header.Get("Content-Type")) {
		w.WriteHeader(resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Error().Err(err).Msg("Failed to read response body")
			return
		}
		body = InjectHTML(body)
		w.Write(body)
		return
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	var err error

	rdClient, err = sdk.NewClient(
		sdk.WithBootstrapServers(bootstrapServers),
		sdk.WithDialer(WebSocketDialerJS()),
	)
	if err != nil {
		panic(err)
	}
	defer rdClient.Close()

	// Expose HTTP handler to JavaScript as __go_jshttp
	js.Global().Set("__go_jshttp", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 1 {
			return js.Global().Get("Promise").Call("reject",
				js.Global().Get("Error").New("required parameter JSRequest missing"))
		}

		jsReq := args[0]
		return httpjs.ServeHTTPAsyncWithStreaming(&Proxy{}, jsReq)
	}))
	log.Info().Msg("Portal proxy handler registered as __go_jshttp")

	go serverWorker(rdClient)

	if runtime.Compiler == "tinygo" {
		return
	}
	// Wait
	ch := make(chan bool)
	<-ch
}

func serverWorker(client *sdk.RDClient) {
	time.Sleep(time.Second)

	cred := sdk.NewCredential()
	ln, err := client.Listen(cred, "WASM-Client-WebServer-"+cred.ID()[:8], []string{"http/1.1"})
	if err != nil {
		log.Error().Err(err).Msg("Failed to start listener")
		return
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
	<title>WASM Client Server</title>
</head>
<body>
	<h1>Hello, World! This server is running in a WASM client</h1>
	<p>Server ID: ` + cred.ID() + `</p>
</body>
</html>`))
	})

	if err := http.Serve(ln, mux); err != nil {
		log.Error().Err(err).Msg("Failed to start server")
	}
}
