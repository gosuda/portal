package main

import (
	"net/http"
	"runtime"
	"syscall/js"

	"github.com/gosuda/portal/cmd/webclient/httpjs"
)

func main() {
	if runtime.Compiler == "tinygo" || runtime.GOARCH != "wasm" {
		return
	}

	// Create HTTP handler
	mux := http.NewServeMux()

	// Register test route
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Portal WebClient - HTTP JS Test</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            max-width: 800px;
            margin: 50px auto;
            padding: 20px;
        }
        .test-section {
            margin: 20px 0;
            padding: 15px;
            border: 1px solid #ddd;
            border-radius: 5px;
        }
        button {
            padding: 10px 20px;
            margin: 5px;
            cursor: pointer;
        }
        #output {
            background: #f5f5f5;
            padding: 10px;
            margin-top: 10px;
            border-radius: 3px;
            white-space: pre-wrap;
            font-family: monospace;
        }
    </style>
</head>
<body>
    <h1>🚀 Portal WebClient - HTTP JS Test</h1>
    <p>Service Worker와 Go WASM이 성공적으로 로드되었습니다!</p>
    
    <div class="test-section">
        <h2>HTTP 요청 테스트</h2>
        <button onclick="testGet()">GET 요청</button>
        <button onclick="testPost()">POST 요청</button>
        <button onclick="testStream()">스트리밍 테스트</button>
        <div id="output"></div>
    </div>

    <script>
        const output = document.getElementById('output');
        
        function log(message) {
            output.textContent += message + '\n';
        }
        
        async function testGet() {
            output.textContent = '';
            log('GET 요청 테스트 시작...');
            try {
                const response = await fetch('/api/test');
                log('Status: ' + response.status);
                log('Headers: ' + JSON.stringify(Object.fromEntries(response.headers)));
                const text = await response.text();
                log('Body: ' + text);
            } catch (err) {
                log('Error: ' + err.message);
            }
        }
        
        async function testPost() {
            output.textContent = '';
            log('POST 요청 테스트 시작...');
            try {
                const data = { message: 'Hello from client!' };
                const response = await fetch('/api/echo', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(data)
                });
                log('Status: ' + response.status);
                const text = await response.text();
                log('Body: ' + text);
            } catch (err) {
                log('Error: ' + err.message);
            }
        }
        
        async function testStream() {
            output.textContent = '';
            log('스트리밍 테스트 시작...');
            try {
                const response = await fetch('/api/stream');
                const reader = response.body.getReader();
                const decoder = new TextDecoder();
                
                while (true) {
                    const { done, value } = await reader.read();
                    if (done) break;
                    const chunk = decoder.decode(value, { stream: true });
                    log('Chunk: ' + chunk);
                }
                log('스트리밍 완료!');
            } catch (err) {
                log('Error: ' + err.message);
            }
        }
    </script>
</body>
</html>
		`))
	})

	// Test API endpoint
	mux.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"success","message":"HTTP JS binding is working!"}`))
	})

	// Echo endpoint
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"received":`))
		w.Write(body[:n])
		w.Write([]byte(`}`))
	})

	// Streaming endpoint
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)

		for i := 1; i <= 5; i++ {
			w.Write([]byte("Chunk " + string(rune('0'+i)) + "\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	})

	// Expose HTTP handler to JavaScript as _portal_http
	js.Global().Set("_portal_http", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 1 {
			return js.Global().Get("Promise").Call("reject",
				js.Global().Get("Error").New("required parameter JSRequest missing"))
		}

		jsReq := args[0]
		return httpjs.ServeHTTPAsyncWithStreaming(mux, jsReq)
	}))

	println("✅ Portal HTTP handler registered as _portal_http")

	// Keep the program running
	ch := make(chan struct{})
	<-ch
}
