package httpsrv

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fsx/internal/identity"
	"fsx/internal/store"
	"fsx/internal/transfer"
	"fsx/internal/ui"
)

type Server struct {
	m       *transfer.Manager
	st      *store.Store
	id      *identity.Identity
	code    string
	version string
	http    int
	p2p     int

	mu   sync.Mutex
	sess map[string]time.Time
}

func New(m *transfer.Manager, st *store.Store, id *identity.Identity, code, version string, httpPort, p2pPort int) *Server {
	return &Server{
		m: m, st: st, id: id, code: code, version: version,
		http: httpPort, p2p: p2pPort, sess: map[string]time.Time{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("POST /api/login", s.login)
	mux.Handle("GET /api/state", s.guard(s.state))
	mux.Handle("GET /api/events", s.guard(s.events))
	mux.Handle("POST /api/send", s.guard(s.send))
	mux.Handle("POST /api/text", s.guard(s.text))
	mux.Handle("POST /api/decision", s.guard(s.decision))
	mux.Handle("POST /api/cancel", s.guard(s.cancel))
	mux.Handle("POST /api/peer", s.guard(s.addPeer))
	mux.Handle("POST /api/upload", s.guard(s.upload))
	mux.Handle("GET /dl/{id}", s.guard(s.download))
	mux.Handle("GET /api/history", s.guard(s.history))
	return mux
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		s.loginPage(w, r)
		return
	}
	var name string
	switch r.URL.Path {
	case "/", "/index.html":
		name = "index.html"
	case "/app.js":
		name = "app.js"
	case "/style.css":
		name = "style.css"
	default:
		http.NotFound(w, r)
		return
	}
	b, err := ui.FS.ReadFile(name)
	if err != nil {
		http.Error(w, "资源缺失", 500)
		return
	}
	switch {
	case strings.HasSuffix(name, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	errMsg := ""
	if r.URL.Query().Get("e") == "1" {
		errMsg = "配对码不正确"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>fsx · 配对</title>
<style>
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0b0d12;color:#e8edf5;font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif}
.card{width:340px;max-width:calc(100vw - 32px);padding:28px;border:1px solid #1e2430;background:#12151c;border-radius:16px}
.logo{width:40px;height:40px;border-radius:11px;background:linear-gradient(135deg,#4c8dff,#2ee6a8);display:grid;place-items:center;font-weight:800;color:#08111f;letter-spacing:-.5px;margin-bottom:18px}
h1{font-size:17px;margin:0 0 6px}p{color:#8b97ab;font-size:13px;margin:0 0 20px}
input{width:100%%;padding:11px 12px;border-radius:9px;border:1px solid #262e3d;background:#0b0d12;color:#e8edf5;font-size:18px;letter-spacing:6px;text-align:center;outline:none}
input:focus{border-color:#4c8dff}
button{width:100%%;margin-top:12px;padding:11px;border:0;border-radius:9px;background:#4c8dff;color:#06101f;font-weight:600;font-size:14px;cursor:pointer}
.err{color:#ff8a8a;font-size:13px;margin-top:12px;text-align:center}
</style></head><body><form class="card" method="post" action="/api/login">
<div class="logo">fsx</div><h1>输入配对码</h1>
<p>配对码显示在运行 fsx 的那台电脑的控制台上。</p>
<input name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="000000" autofocus>
<button type="submit">连接</button>%s</form></body></html>`,
		func() string {
			if errMsg != "" {
				return `<div class="err">` + errMsg + `</div>`
			}
			return ""
		}())
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if r.FormValue("code") != s.code || s.code == "" {
		http.Redirect(w, r, "/?e=1", http.StatusSeeOther)
		return
	}
	b := make([]byte, 16)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.sess[tok] = time.Now().Add(30 * 24 * time.Hour)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "fsx_sess", Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) authed(r *http.Request) bool {
	if isLoopback(r) {
		return true
	}
	c, err := r.Cookie("fsx_sess")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sess[c.Value]
	return ok && time.Now().Before(exp)
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) guard(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeJSON(w, 401, map[string]string{"error": "未授权，请先输入配对码"})
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("X-FSX") != "1" {
			writeJSON(w, 403, map[string]string{"error": "缺少请求标识"})
			return
		}
		h(w, r)
	})
}

func (s *Server) buildState(r *http.Request) map[string]any {
	snap := s.m.Snapshot()
	cfg := s.st.Config()
	self := map[string]any{
		"id": s.id.ID, "short": identity.Short(s.id.ID), "name": cfg.Name,
		"version": s.version, "http_port": s.http, "p2p_port": s.p2p,
		"download_dir": cfg.DownloadDir,
	}
	out := map[string]any{
		"self": self, "peers": snap.Peers, "jobs": snap.Jobs,
		"messages": snap.Messages, "relay": snap.Relay,
	}
	if isLoopback(r) {
		out["code"] = s.code
	}
	return out
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.buildState(r))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		b, err := json.Marshal(s.buildState(r))
		if err == nil {
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer  string   `json:"peer"`
		Addr  string   `json:"addr"`
		Paths []string `json:"paths"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	if len(req.Paths) == 0 {
		writeJSON(w, 400, errBody(fmt.Errorf("没有文件；网页端请用上传发送")))
		return
	}
	peer, addr := splitTarget(req.Peer, req.Addr)
	id, err := s.m.SendTo(peer, addr, req.Paths, nil)
	if err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	writeJSON(w, 200, map[string]string{"id": id})
}

// splitTarget 允许把 IP:端口 直接写在 peer 字段，便于脚本调用。
func splitTarget(peer, addr string) (string, string) {
	peer = strings.TrimSpace(peer)
	addr = strings.TrimSpace(addr)
	if addr == "" && strings.Contains(peer, ":") {
		return "", peer
	}
	return peer, addr
}

func (s *Server) text(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer string `json:"peer"`
		Addr string `json:"addr"`
		Body string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSON(w, 400, errBody(fmt.Errorf("内容为空")))
		return
	}
	peer, addr := splitTarget(req.Peer, req.Addr)
	if err := s.m.SendText(peer, addr, req.Body); err != nil {
		writeJSON(w, 502, errBody(err))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) decision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Accept bool   `json:"accept"`
		Always bool   `json:"always"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	if err := s.m.Decide(req.ID, req.Accept, req.Always); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	s.m.Cancel(req.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) addPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	addr := strings.TrimSpace(req.Addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		writeJSON(w, 400, errBody(fmt.Errorf("地址格式应为 IP:端口")))
		return
	}
	s.m.AddManual(net.JoinHostPort(host, port))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, 400, errBody(fmt.Errorf("需要 multipart 上传")))
		return
	}
	tmp, err := os.MkdirTemp(s.st.Dir(), "upload-")
	if err != nil {
		writeJSON(w, 500, errBody(err))
		return
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()
	var peer, addr string
	var paths []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJSON(w, 400, errBody(err))
			return
		}
		switch p.FormName() {
		case "peer":
			b, _ := io.ReadAll(io.LimitReader(p, 256))
			peer = strings.TrimSpace(string(b))
		case "addr":
			b, _ := io.ReadAll(io.LimitReader(p, 256))
			addr = strings.TrimSpace(string(b))
		case "files":
			name := filepath.Base(strings.ReplaceAll(p.FileName(), "\\", "/"))
			if name == "" || name == "." || name == ".." {
				continue
			}
			dst := filepath.Join(tmp, name)
			f, err := os.Create(dst)
			if err != nil {
				writeJSON(w, 500, errBody(err))
				return
			}
			_, cerr := io.Copy(f, p)
			f.Close()
			if cerr != nil {
				writeJSON(w, 400, errBody(cerr))
				return
			}
			paths = append(paths, dst)
		}
		p.Close()
	}
	if len(paths) == 0 {
		writeJSON(w, 400, errBody(fmt.Errorf("没有收到文件")))
		return
	}
	peer, addr = splitTarget(peer, addr)
	id, err := s.m.SendTo(peer, addr, paths, func() { os.RemoveAll(tmp) })
	if err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	ok = true
	writeJSON(w, 200, map[string]string{"id": id})
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j := s.m.FindJob(id)
	if j == nil || j.Dir != "recv" {
		http.NotFound(w, r)
		return
	}
	var files []string
	for _, it := range j.Items {
		if it.Finalized {
			files = append(files, it.FinalPath)
		}
	}
	if len(files) == 0 {
		http.Error(w, "文件尚未接收完成", 409)
		return
	}
	if idx := r.URL.Query().Get("i"); idx != "" {
		var n int
		if _, err := fmt.Sscanf(idx, "%d", &n); err != nil || n < 0 || n >= len(files) {
			http.Error(w, "索引越界", 400)
			return
		}
		serveFile(w, r, files[n])
		return
	}
	if len(files) == 1 {
		serveFile(w, r, files[0])
		return
	}
	root := s.st.Config().DownloadDir
	w.Header().Set("Content-Type", "application/zip")
	setDownloadName(w, "fsx-"+id+".zip")
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, p := range files {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		fw, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		io.Copy(fw, f)
		f.Close()
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "读取失败", 500)
		return
	}
	setDownloadName(w, filepath.Base(path))
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}

func setDownloadName(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.st.History())
}

func readJSON(r *http.Request, v any) error {
	defer io.Copy(io.Discard, r.Body)
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errBody(err error) map[string]string { return map[string]string{"error": err.Error()} }
