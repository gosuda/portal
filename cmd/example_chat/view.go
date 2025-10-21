package main

import (
	"context"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// simple in-memory chat hub
type hub struct {
	mu       sync.RWMutex
	messages []message
	conns    map[*websocket.Conn]struct{}
}

type message struct {
	TS   time.Time `json:"ts"`
	User string    `json:"user"`
	Text string    `json:"text"`
}

func newHub() *hub {
	return &hub{conns: map[*websocket.Conn]struct{}{}, messages: make([]message, 0, 64)}
}

func (h *hub) broadcast(m message) {
	h.mu.Lock()
	h.messages = append(h.messages, m)
	conns := make([]*websocket.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = wsjson.Write(ctx, c, m)
		cancel()
	}
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
	if len(backlog) > 20 {
		backlog = backlog[len(backlog)-20:]
	}
	for _, m := range backlog {
		_ = wsjson.Write(connCtx, conn, m)
	}
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.conns, conn)
			h.mu.Unlock()
			conn.Close(websocket.StatusNormalClosure, "")
			cancelConn()
		}()
		for {
			var req struct {
				User string `json:"user"`
				Text string `json:"text"`
			}
			if err := wsjson.Read(connCtx, conn, &req); err != nil {
				return
			}
			if req.User == "" {
				req.User = "anon"
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
    body { margin:0; padding:24px; background:var(--bg); color:var(--fg); font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial }
    .wrap { max-width: 920px; margin: 0 auto }
    h1 { margin:0 0 12px 0; font-weight:700 }
    .term { border:1px solid var(--border); border-radius:10px; background:var(--panel); overflow:hidden }
    .termbar { display:flex; align-items:center; justify-content:space-between; padding:10px 12px; border-bottom:1px solid var(--border); font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size:14px }
    .dots { display:flex; gap:6px }
    .dot { width:10px; height:10px; border-radius:50%; }
    .dot.red{ background:#ef4444 }
    .dot.yellow{ background:#f59e0b }
    .dot.green{ background:#22c55e }
    .nick { display:flex; align-items:center; gap:8px }
    .nick input{ background:transparent; border:1px solid var(--border); color:var(--fg); padding:6px 8px; border-radius:6px; font-family:inherit; font-size:13px; width:180px }
    .nick button{ background:transparent; border:1px solid var(--border); color:var(--fg); padding:6px 8px; border-radius:6px; font-family:inherit; font-size:13px; cursor:pointer }
    .screen { height:420px; overflow:auto; padding:14px; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size:14px; line-height:1.5; }
    .line { white-space: pre-wrap; word-break: break-word }
    .ts { color:var(--muted) }
    .usr { color:#60a5fa }
    .promptline { display:flex; align-items:center; gap:8px; padding:12px 14px; border-top:1px solid var(--border); font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    #prompt { color:var(--accent) }
    #cmd { flex:1; background:transparent; border:none; outline:none; color:var(--fg); font-family: inherit; font-size:14px; caret-color: var(--cursor) }
    small{ color:var(--muted); display:block; margin-top:10px }
  </style>
</head>
<body>
  <div class="wrap">
    <h1>🔐 Chatting — {{.Name}}</h1>
    <div class="term">
      <div class="termbar">
        <div class="dots"><span class="dot red"></span><span class="dot yellow"></span><span class="dot green"></span></div>
        <div style="opacity:.9">relaychat@relaydns</div>
        <div class="nick">
          <label for="user" style="color:var(--muted)">nick</label>
          <input id="user" type="text" placeholder="anon" />
          <button id="roll" title="randomize nickname">🎲</button>
        </div>
      </div>
      <div id="log" class="screen"></div>
      <div class="promptline">
        <span id="prompt"></span>
        <input id="cmd" type="text" autocomplete="off" spellcheck="false" placeholder="type a message and press Enter" />
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

    function setPrompt(){
      const nick = (user.value || 'anon').replace(/\s+/g,'').slice(0,24) || 'anon';
      promptEl.textContent = nick + '@chat:~$';
    }
    function randomNick(){
      // Programming-meme themed nickname
      const techs = ['gopher','rustacean','nixer','unix','kernel','docker','kube','vim','emacs','tmux','nvim','git','linux','bsd','wasm','grpc','lambda','pointer','monad','segfault','null','byte','packet','devops','cli'];
      const roles = ['wizard','hacker','guru','daemon','runner','scripter','shell','warrior','artisan','smith'];
      const a = techs[Math.floor(Math.random()*techs.length)];
      const b = roles[Math.floor(Math.random()*roles.length)];
      const id = Math.random().toString(36).slice(2,6);
      return a + '-' + b + '-' + id;
    }
    // Restore nickname or initialize randomly
    let savedNick = null;
    try { savedNick = localStorage.getItem('relaydns_nick'); } catch(_) {}
    if(savedNick){
      user.value = savedNick;
    } else {
      user.value = randomNick();
      try { localStorage.setItem('relaydns_nick', user.value); } catch(_) {}
    }
    setPrompt();
    user.addEventListener('input', () => { try{ localStorage.setItem('relaydns_nick', user.value); }catch(_){}; setPrompt(); });
    roll.addEventListener('click', () => {
      user.value = randomNick();
      try{ localStorage.setItem('relaydns_nick', user.value); }catch(_){}
      setPrompt();
      user.focus();
    });

    // Stable color per nickname
    const PALETTE = ['#60a5fa','#22c55e','#f59e0b','#ef4444','#a78bfa','#14b8a6','#eab308','#f472b6','#8b5cf6','#06b6d4'];
    function hashNick(s){
      let h = 0;
      for (let i = 0; i < s.length; i++) { h = ((h << 5) - h) + s.charCodeAt(i); h |= 0; }
      return (h >>> 0);
    }
    function colorFor(nick){
      const idx = hashNick(nick || 'anon') % PALETTE.length;
      return PALETTE[idx];
    }
    function append(msg){
      const div = document.createElement('div');
      div.className = 'line';
      const ts = new Date(msg.ts).toLocaleTimeString();
      const nick = (msg.user || 'anon');
      const color = colorFor(nick);
      div.innerHTML = '<span class="ts">[' + ts + ']</span> <span class="usr" style="color:' + color + '">' +
        nick + '</span>: ' + escapeHTML(msg.text || '');
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
    function send(){
      const payload = { user: (user.value || 'anon'), text: cmd.value.trim() };
      if(!payload.text) return;
      ws.send(JSON.stringify(payload));
      cmd.value='';
    }
    cmd.addEventListener('keydown', e => {
      if(e.key === 'Enter') { e.preventDefault(); send(); }
    });
    // Focus command line on load
    setTimeout(()=>cmd.focus(), 0);
  </script>
</body>
</html>`))
