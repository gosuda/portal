package main

import (
	"context"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog/log"
)

// simple in-memory chat hub
type hub struct {
	mu        sync.RWMutex
	messages  []message
	conns     map[*websocket.Conn]struct{}
	connUID   map[*websocket.Conn]string
	userConns map[string]map[*websocket.Conn]struct{}
	userName  map[string]string
	wg        sync.WaitGroup
	store     *messageStore
}

type message struct {
	TS    time.Time `json:"ts"`
	User  string    `json:"user"`
	Text  string    `json:"text"`
	Event string    `json:"event,omitempty"` // "joined" | "left" | "roster"
	UID   string    `json:"uid,omitempty"`
	Users []string  `json:"users,omitempty"`
}

func newHub() *hub {
	return &hub{
		conns:     map[*websocket.Conn]struct{}{},
		connUID:   map[*websocket.Conn]string{},
		userConns: map[string]map[*websocket.Conn]struct{}{},
		userName:  map[string]string{},
		messages:  make([]message, 0, 64),
	}
}

func (h *hub) broadcast(m message) {
	h.mu.Lock()
	// Do not persist/retain roster messages in backlog; they are ephemeral UI state
	if m.Event != "roster" {
		h.messages = append(h.messages, m)
	}
	conns := make([]*websocket.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	if h.store != nil && m.Event != "roster" {
		if err := h.store.Append(m); err != nil {
			log.Debug().Err(err).Msg("persist message")
		}
	}
	for _, c := range conns {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = wsjson.Write(ctx, c, m)
		cancel()
	}
}

// broadcastRoster sends the current list of connected user names to all clients.
func (h *hub) broadcastRoster() {
	// Build roster snapshot
	h.mu.RLock()
	users := make([]string, 0, len(h.userName))
	for uid, name := range h.userName {
		if set, ok := h.userConns[uid]; !ok || len(set) == 0 {
			continue
		}
		if name == "" {
			name = "anon"
		}
		users = append(users, name)
	}
	h.mu.RUnlock()
	// Sort for stable UI order
	sort.Strings(users)
	h.broadcast(message{TS: time.Now().UTC(), Event: "roster", Users: users})
}

// attachStore connects a persistent store to the hub.
func (h *hub) attachStore(s *messageStore) {
	h.mu.Lock()
	h.store = s
	h.mu.Unlock()
}

// bootstrap preloads history into the in-memory buffer.
func (h *hub) bootstrap(msgs []message) {
	h.mu.Lock()
	h.messages = append(h.messages, msgs...)
	h.mu.Unlock()
}

// closeAll force-closes all active websocket connections (used during shutdown).
func (h *hub) closeAll() {
	h.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		_ = c.Close(websocket.StatusGoingAway, "server shutdown")
	}
}

// wait blocks until all websocket handler goroutines have finished.
func (h *hub) wait() {
	h.wg.Wait()
}

func handleWS(w http.ResponseWriter, r *http.Request, h *hub) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allow any origin for demo simplicity. Consider tightening in production.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	// Use a connection-scoped context not tied to the HTTP request lifecycle.
	connCtx, cancelConn := context.WithCancel(context.Background())
	h.mu.Lock()
	h.conns[conn] = struct{}{}
	backlog := append([]message(nil), h.messages...)
	h.mu.Unlock()

	for _, m := range backlog {
		_ = wsjson.Write(connCtx, conn, m)
	}
	h.wg.Add(1)
	go func() {
		defer func() {
			var leftUser string
			var uid string
			var lastConn bool
			h.mu.Lock()
			uid = h.connUID[conn]
			if uid != "" {
				if set, ok := h.userConns[uid]; ok {
					delete(set, conn)
					if len(set) == 0 {
						lastConn = true
						delete(h.userConns, uid)
					} else {
						h.userConns[uid] = set
					}
				}
				leftUser = h.userName[uid]
				if lastConn {
					delete(h.userName, uid)
				}
				delete(h.connUID, conn)
			}
			delete(h.conns, conn)
			h.mu.Unlock()
			if leftUser != "" && lastConn {
				h.broadcast(message{TS: time.Now().UTC(), User: leftUser, Event: "left"})
				h.broadcastRoster()
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
			cancelConn()
			h.wg.Done()
		}()
		for {
			var req struct {
				User string `json:"user"`
				Text string `json:"text"`
				UID  string `json:"uid"`
			}
			if err := wsjson.Read(connCtx, conn, &req); err != nil {
				return
			}
			if req.User == "" {
				req.User = "anon"
			}
			if req.UID == "" {
				// Fallback to a per-connection unique id if client didn't provide one
				req.UID = strconv.FormatInt(time.Now().UnixNano(), 10)
			}
			// map connection to uid and maintain per-user state
			var announce bool
			var renamed bool
			var prevName string
			h.mu.Lock()
			if _, ok := h.connUID[conn]; !ok {
				h.connUID[conn] = req.UID
				if _, ok := h.userConns[req.UID]; !ok {
					h.userConns[req.UID] = map[*websocket.Conn]struct{}{}
				}
				if len(h.userConns[req.UID]) == 0 {
					announce = true
				}
				h.userConns[req.UID][conn] = struct{}{}
			}
			if cur, ok := h.userName[req.UID]; !ok {
				h.userName[req.UID] = req.User
			} else if cur != req.User {
				prevName = cur
				h.userName[req.UID] = req.User
				renamed = true
			}
			h.mu.Unlock()
			if announce {
				h.broadcast(message{TS: time.Now().UTC(), User: req.User, Event: "joined"})
				h.broadcastRoster()
			} else if renamed {
				// Announce rename as an event line in chat
				h.broadcast(message{TS: time.Now().UTC(), User: prevName, Text: req.User, Event: "rename"})
				h.broadcastRoster()
			}
			if req.Text == "" {
				continue
			}
			h.broadcast(message{TS: time.Now().UTC(), User: req.User, Text: req.Text})
		}
	}()
}

func serveIndex(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTmpl.Execute(w, struct{ Name string }{Name: name})
}

// serveChatHTTP starts serving the chat UI and websocket endpoint and returns the server.
// Callers are responsible for shutting it down via Server.Shutdown.
func serveChatHTTP(ln net.Listener, name string, h *hub) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { serveIndex(w, r, name) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { handleWS(w, r, h) })

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	log.Info().Msgf("[chat] http listening on %s", ln.Addr().String())

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("chat http error")
		}
	}()
	return srv
}

var indexTmpl = template.Must(template.New("chat").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>RelayDNS Chat — {{.Name}}</title>
  <style>
    :root{
      --bg: #0d1117;
      --panel: #111827;
      --border: #1f2937;
      --fg: #e5e7eb;
      --muted: #9ca3af;
      --accent: #22c55e;
      --cursor: #22c55e;
    }
    *{ box-sizing: border-box }
    /* Prefer locally-installed D2Coding for Korean monospaced rendering */
    @font-face { font-family: 'D2Coding'; src: local('D2Coding'), local('D2Coding Ligature'), local('D2Coding Nerd'); font-display: swap; }
    body { margin:0; padding:24px; background:var(--bg); color:var(--fg); font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial }
    .wrap { max-width: 920px; margin: 0 auto }
    h1 { margin:0 0 12px 0; font-weight:700 }
    .term { border:1px solid var(--border); border-radius:10px; background:var(--panel); overflow:hidden; position: relative }
    .termbar { display:flex; align-items:center; justify-content:space-between; padding:10px 12px; border-bottom:1px solid var(--border); font-family: 'D2Coding', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size:14px }
    .termbar-left { display:flex; align-items:center; gap:8px }
    .termbar-center { flex:1 1 auto; display:flex; justify-content:center }
    .term-actions { display:flex; align-items:center; gap:8px }
    .dots { display:flex; gap:6px }
    .dot { width:10px; height:10px; border-radius:50%; }
    .dot.red{ background:#ef4444 }
    .dot.yellow{ background:#f59e0b }
    .dot.green{ background:#22c55e }
    .nick { display:flex; align-items:center; gap:8px }
    .nick input{ background:transparent; border:1px solid var(--border); color:var(--fg); padding:6px 8px; border-radius:6px; font-family:inherit; font-size:13px; width:180px }
    .nick button{ background:transparent; border:1px solid var(--border); color:var(--fg); padding:6px 8px; border-radius:6px; font-family:inherit; font-size:13px; cursor:pointer }
    .screen { height:420px; overflow:auto; padding:14px; font-family: 'D2Coding', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size:14px; line-height:1.5; }
    .line { white-space: pre-wrap; word-break: break-word }
    .ts { color:var(--muted) }
    .usr { color:#60a5fa }
    .event { color: var(--muted) }
    .event .usr { color: var(--muted) }
    .promptline { display:flex; align-items:center; gap:8px; padding:12px 14px; border-top:1px solid var(--border); font-family: 'D2Coding', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    #prompt { color:var(--accent) }
    #cmd { flex:1 1 auto; min-width:0; background:transparent; border:none; outline:none; color:var(--fg); font-family: inherit; font-size:14px; caret-color: var(--cursor) }
    small{ color:var(--muted); display:block; margin-top:10px }

    /* Scrollbar styling for log and users list */
    .screen { scrollbar-width: thin; scrollbar-color: #374151 #0d1117; }
    .screen::-webkit-scrollbar { width: 10px }
    .screen::-webkit-scrollbar-track { background: #0d1117 }
    .screen::-webkit-scrollbar-thumb { background: #374151; border-radius: 8px; border: 2px solid #111827 }
    .screen::-webkit-scrollbar-thumb:hover { background: #4b5563 }
    .userspill { display:inline-block; border:1px solid var(--border); padding:2px 10px; border-radius:999px; color:var(--fg); font-size:12px; opacity:.9 }

    /* Mobile responsiveness */
    @media (max-width: 640px) {
      body { padding: 12px; }
      .wrap { max-width: 100%; }
      h1 { font-size: 18px; }
      .termbar { flex-wrap: wrap; gap: 8px; }
      .termbar-center { order: 2; width: 100%; justify-content: center; }
      .promptline { flex-direction: column; align-items: stretch; gap: 10px; }
      #prompt { order: 1; }
      #cmd { order: 2; font-size: 16px; }
      .nick { order: 3; width: 100%; }
      .nick input { width: 100%; font-size: 16px; }
      .nick button { font-size: 16px; }
      .screen { height: 50vh; font-size: 13px; }
      .promptline { flex-wrap: nowrap; }
    }
  </style>
</head>
<body>
  <div class="wrap">
    <h1>🔐 Chatting — {{.Name}}</h1>
    <div class="term">
      <div class="termbar">
        <div class="termbar-left">
          <div class="dots"><span class="dot red"></span><span class="dot yellow"></span><span class="dot green"></span></div>
        </div>
        <div class="termbar-center">
          <div class="nick">
            <label for="user" style="color:var(--muted)">nickname</label>
            <input id="user" type="text" placeholder="anon" />
            <button id="roll" title="randomize nickname">🎲</button>
          </div>
        </div>
        <div class="term-actions"><span class="userspill"><span id="users-count">0</span> Online</span></div>
      </div>
      <div id="log" class="screen"></div>
      <div class="promptline">
        <span id="prompt"></span>
        <input id="cmd" type="text" autocomplete="off" spellcheck="false" placeholder="type a message and press Enter" enterkeyhint="send" inputmode="text" />
      </div>
    </div>
    <small>Tip: Enter to send • Nickname persists locally</small>
  </div>
  <script>
    const log = document.getElementById('log');
    const user = document.getElementById('user');
    const cmd = document.getElementById('cmd');
    const roll = document.getElementById('roll');
    const promptEl = document.getElementById('prompt');
    const usersCount = document.getElementById('users-count');

    function setPrompt(){
      const nick = (user.value || 'anon').replace(/\s+/g,'').slice(0,24) || 'anon';
      promptEl.textContent = nick + '@chat:~$';
    }
    function randomNick(){
      // Short nickname: one word + 4-digit number
      const words = ['gopher','rust','unix','kernel','docker','kube','vim','emacs','tmux','nvim','git','linux','bsd','wasm','grpc','lambda','pointer','monad','null','byte','packet','devops','cli'];
      const w = words[Math.floor(Math.random()*words.length)];
      const num = Math.floor(Math.random()*9000) + 1000; // 4-digit
      return w + '-' + num;
    }
    // Stable client UID per browser (per origin)
    function genUID(){ try{ return (crypto.randomUUID && crypto.randomUUID()) || '' }catch(_){ return '' } }
    function fallbackUID(){ return Math.random().toString(36).slice(2) + Date.now().toString(36) }
    let clientUID = null;
    try { clientUID = localStorage.getItem('relaydns_uid'); } catch(_) {}
    if(!clientUID || clientUID.length < 8){ clientUID = genUID() || fallbackUID(); try { localStorage.setItem('relaydns_uid', clientUID); } catch(_) {} }

    // Restore nickname or initialize randomly
    let savedNick = null;
    try { savedNick = localStorage.getItem('relaydns_nick'); } catch(_) {}
    if(savedNick){
      const oldPattern = /^[a-z]+-[a-z]+-[0-9a-z]{2,}$/i.test(savedNick);
      if (oldPattern) {
        user.value = randomNick();
        try { localStorage.setItem('relaydns_nick', user.value); } catch(_) {}
      } else {
        user.value = savedNick;
      }
    } else {
      user.value = randomNick();
      try { localStorage.setItem('relaydns_nick', user.value); } catch(_) {}
    }
    setPrompt();
    // Debounced notify of nickname changes to server so roster updates without sending a chat
    let nickTimer = null;
    user.addEventListener('input', () => {
      try{ localStorage.setItem('relaydns_nick', user.value); }catch(_){}
      setPrompt();
      if (ws && ws.readyState === 1) {
        if (nickTimer) clearTimeout(nickTimer);
        nickTimer = setTimeout(() => {
          try{ ws.send(JSON.stringify({ user: (user.value || 'anon'), text: '', uid: clientUID })); }catch(_){ }
        }, 300);
      }
    });
    roll.addEventListener('click', () => {
      user.value = randomNick();
      try{ localStorage.setItem('relaydns_nick', user.value); }catch(_){}
      setPrompt();
      user.focus();
    });

    // Stable color per nickname (expanded palette)
    const PALETTE = [
      '#60a5fa','#22c55e','#f59e0b','#ef4444','#a78bfa','#14b8a6','#eab308','#f472b6','#8b5cf6','#06b6d4',
      '#34d399','#fb7185','#c084fc','#f97316','#84cc16','#10b981','#38bdf8','#f43f5e','#e879f9','#fde047',
      '#93c5fd','#4ade80','#fca5a5','#a3e635','#67e8f9','#f0abfc','#fbbf24','#86efac'
    ];
    function hashNick(s){
      let h = 0;
      for (let i = 0; i < s.length; i++) { h = ((h << 5) - h) + s.charCodeAt(i); h |= 0; }
      return (h >>> 0);
    }
    function colorFor(nick){
      const idx = hashNick(nick || 'anon') % PALETTE.length;
      return PALETTE[idx];
    }
    function renderRoster(users){
      const count = (users ? users.length : 0);
      if (usersCount) usersCount.textContent = String(count);
    }
    function append(msg){
      const div = document.createElement('div');
      div.className = 'line';
      const ts = new Date(msg.ts).toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
      const nick = (msg.user || 'anon');
      const color = colorFor(nick);
      if (msg.event === 'roster') { renderRoster(msg.users || []); return; }
      if (msg.event === 'rename') {
        div.className = 'line event';
        div.innerHTML = '<span class="ts">[' + ts + ']</span> ' + escapeHTML(msg.user || 'anon') + ' -> ' + escapeHTML(msg.text || '') + ' changed';
      } else if (msg.event === 'joined' || msg.event === 'left') {
        const verb = msg.event === 'joined' ? 'joined' : 'left';
        div.className = 'line event';
        div.innerHTML = '<span class="ts">[' + ts + ']</span> ' + escapeHTML(nick) + ' ' + verb;
      } else {
        div.innerHTML = '<span class="ts">[' + ts + ']</span> <span class="usr" style="color:' + color + '">' +
          nick + '</span>: ' + escapeHTML(msg.text || '');
      }
      log.appendChild(div);
      log.scrollTop = log.scrollHeight;
    }
    function escapeHTML(s){
      return s.replace(/[&<>\"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','\"':'&quot;'}[c]));
    }

    const wsProto = location.protocol === 'https:' ? 'wss' : 'ws';
    const basePath = location.pathname.endsWith('/') ? location.pathname : (location.pathname + '/');
    const ws = new WebSocket(wsProto + '://' + location.host + basePath + 'ws');
    ws.onmessage = (e) => { try{ append(JSON.parse(e.data)); }catch(_){ } };
    ws.onopen = () => {
      try{ ws.send(JSON.stringify({ user: (user.value || 'anon'), text: '', uid: clientUID })); }catch(_){ }
    };
    function send(){
      const payload = { user: (user.value || 'anon'), text: cmd.value.trim(), uid: clientUID };
      if(!payload.text) return;
      ws.send(JSON.stringify(payload));
      cmd.value='';
    }
    // Handle IME composition properly to avoid duplicated last character
    // on Enter when using Korean/Japanese/Chinese input methods.
    cmd.addEventListener('keydown', e => {
      if (e.isComposing || e.keyCode === 229) { return; }
      if (e.key === 'Enter') {
        e.preventDefault();
        e.stopPropagation();
        send();
        // Ensure focus stays on the message input on mobile
        setTimeout(() => cmd.focus(), 0);
      }
    });
    // Focus command line on load
    setTimeout(()=>cmd.focus(), 0);
  </script>
</body>
</html>`))
