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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fsx/internal/addrs"
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
	mux.Handle("POST /api/reveal", s.guard(s.reveal))
	mux.Handle("POST /api/probe", s.guard(s.probe))
	mux.Handle("GET /api/history", s.guard(s.history))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-FSX-Server", "fsx")
		mux.ServeHTTP(w, r)
	})
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
	errHTML := ""
	if r.URL.Query().Get("e") == "1" {
		errHTML = `<div class="err">配对码不正确，请看运行 fsx 那台电脑的托盘菜单</div>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, loginHTML, errHTML)
}

const loginHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#07090D"><meta name="color-scheme" content="dark">
<title>fsx · 配对</title>
<style>
*{box-sizing:border-box}
html,body{height:100%%}
body{margin:0;display:grid;place-items:center;background:#07090D;color:#E9EFF8;overflow:hidden;
  font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
.bg{position:fixed;inset:0;pointer-events:none;overflow:hidden}
.bg i{position:absolute;border-radius:50%%;filter:blur(80px);opacity:.55}
.bg i:first-child{width:60vw;height:60vw;left:-18vw;top:-26vw;background:radial-gradient(circle,rgba(76,141,255,.55),transparent 66%%);animation:d1 30s ease-in-out infinite}
.bg i:last-child{width:48vw;height:48vw;right:-16vw;bottom:-22vw;background:radial-gradient(circle,rgba(46,230,168,.34),transparent 66%%);animation:d2 38s ease-in-out infinite}
.bg u{position:absolute;inset:0;background-image:linear-gradient(rgba(255,255,255,.02) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.02) 1px,transparent 1px);background-size:44px 44px;
  -webkit-mask-image:radial-gradient(110%% 80%% at 50%% 0%%,#000 20%%,transparent 76%%);mask-image:radial-gradient(110%% 80%% at 50%% 0%%,#000 20%%,transparent 76%%)}
@keyframes d1{0%%,100%%{transform:translate(0,0) scale(1)}50%%{transform:translate(5vw,4vh) scale(1.1)}}
@keyframes d2{0%%,100%%{transform:translate(0,0) scale(1.05)}50%%{transform:translate(-4vw,-4vh) scale(.95)}}
.card{position:relative;width:372px;max-width:calc(100vw - 28px);padding:28px;border:1px solid #28344A;border-radius:20px;
  background:linear-gradient(180deg,#131822,#0A0D13);box-shadow:0 40px 100px -50px #000,0 1px 0 rgba(255,255,255,.04) inset;
  animation:up .5s cubic-bezier(.2,.8,.2,1) both}
@keyframes up{from{opacity:0;transform:translateY(14px)}to{opacity:1;transform:none}}
.brand{display:flex;align-items:center;gap:10px;margin-bottom:22px}
.mark{width:34px;height:34px;border-radius:11px;display:grid;place-items:center;background:linear-gradient(135deg,#4C8DFF,#2EE6A8);box-shadow:0 8px 22px -10px rgba(76,141,255,.95)}
.mark svg{width:21px;height:21px;fill:#06121C}
.word{font-size:19px;font-weight:700;letter-spacing:.02em;color:#F0E2CE}
.tag{margin-left:auto;font-size:10px;letter-spacing:.14em;text-transform:uppercase;color:#7E8EA6}
h1{font-size:18px;margin:0 0 6px;letter-spacing:-.01em}
p{color:#A9B7CB;font-size:12.5px;margin:0 0 20px}
input{width:100%%;padding:14px 12px;border-radius:12px;border:1px solid #28344A;background:#07090D;color:#fff;
  font-family:"SF Mono",ui-monospace,Menlo,Consolas,monospace;font-size:26px;font-weight:600;letter-spacing:.3em;text-indent:.3em;text-align:center;outline:none;transition:.18s}
input::placeholder{color:#39445A;letter-spacing:.3em}
input:focus{border-color:rgba(76,141,255,.7);box-shadow:0 0 0 3px rgba(76,141,255,.22)}
button{width:100%%;margin-top:14px;padding:12px;border:0;border-radius:11px;background:linear-gradient(135deg,#4C8DFF,#2EE6A8);
  color:#05121C;font-weight:700;font-size:14px;cursor:pointer;transition:.16s;box-shadow:0 12px 30px -16px rgba(76,141,255,1)}
button:hover{filter:brightness(1.08);transform:translateY(-1px)}
button:active{transform:none}
.foot{display:flex;align-items:center;gap:7px;margin-top:18px;padding-top:14px;border-top:1px solid #1C2432;color:#7E8EA6;font-size:11px}
.foot svg{width:13px;height:13px;fill:none;stroke:currentColor;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round}
.err{margin-top:14px;padding:9px 11px;border:1px solid rgba(255,107,129,.4);border-radius:10px;background:rgba(255,107,129,.1);color:#FF6B81;font-size:12px;text-align:center}
</style></head><body>
<div class="bg"><i></i><i></i><u></u></div>
<form class="card" method="post" action="/api/login">
  <div class="brand">
    <span class="mark"><svg viewBox="0 0 32 32"><path d="M16 7l8 9h-5v9h-6v-9H8z"/></svg></span>
    <span class="word">fsx</span><span class="tag">pair</span>
  </div>
  <h1>输入配对码</h1>
  <p>配对码在那台电脑的 fsx 托盘菜单里，本机控制台首页也会显示。</p>
  <input name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="000000" autofocus aria-label="配对码">
  <button type="submit">连接节点</button>
  %s
  <div class="foot"><svg viewBox="0 0 24 24"><rect x="4.5" y="10.5" width="15" height="9.5" rx="2.5"/><path d="M8 10.5V8a4 4 0 0 1 8 0v2.5"/></svg>配对后本机记住此设备 30 天</div>
</form></body></html>`

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

// localIP 给配对提示用：UDP dial 不发包，只让内核选出出口网卡地址。
func localIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil && !a.IP.IsLoopback() {
		return a.IP.String()
	}
	return ""
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
		"download_dir": cfg.DownloadDir, "lan": localIP(), "v6": addrs.GlobalV6(),
	}
	out := map[string]any{
		"self": self, "peers": snap.Peers, "jobs": snap.Jobs,
		"messages": snap.Messages, "relay": snap.Relay,
		"local": isLoopback(r),
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
		ID   string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	target := strings.TrimSpace(req.ID)
	if target == "" {
		target = strings.TrimSpace(req.Addr)
	}
	pid, addr, name, err := s.m.Resolve(target)
	if err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	if pid == "" {
		host, port, e := net.SplitHostPort(addr)
		if e != nil || host == "" || port == "" {
			writeJSON(w, 400, errBody(fmt.Errorf("地址格式应为 IP:端口")))
			return
		}
		s.m.AddManual(net.JoinHostPort(host, port))
	} else {
		s.m.AddPeer(pid, name, addr)
	}
	writeJSON(w, 200, map[string]string{"id": pid, "addr": addr, "name": name})
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

func (s *Server) reveal(w http.ResponseWriter, r *http.Request) {
	// 打开的是节点本机的文件管理器；远程控制台调用只会开在节点上，故禁止。
	if !isLoopback(r) {
		writeJSON(w, 403, map[string]string{"error": "仅本机控制台可打开文件夹"})
		return
	}
	var req struct {
		ID string `json:"id"`
		I  int    `json:"i"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, errBody(err))
		return
	}
	j := s.m.FindJob(req.ID)
	if j == nil || j.Dir != "recv" {
		writeJSON(w, 404, map[string]string{"error": "任务不存在"})
		return
	}
	var files []string
	for _, it := range j.Items {
		if it.Finalized {
			files = append(files, it.FinalPath)
		}
	}
	if len(files) == 0 {
		writeJSON(w, 409, map[string]string{"error": "文件尚未接收完成"})
		return
	}
	if req.I < 0 || req.I >= len(files) {
		writeJSON(w, 400, map[string]string{"error": "索引越界"})
		return
	}
	if err := revealPath(files[req.I]); err != nil {
		writeJSON(w, 500, map[string]string{"error": "打开文件夹失败: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func revealPath(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", path).Run()
	case "windows":
		// explorer 即使成功也常返回非零退出码，故只启动不等待。
		return exec.Command("explorer", "/select,"+path).Start()
	default:
		return exec.Command("xdg-open", filepath.Dir(path)).Run()
	}
}

// probe 做一次短超时 TCP 连通性探测，供界面「检测 IPv6 直通」使用。
func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
	}
	if err := readJSON(r, &req); err != nil || req.Addr == "" {
		writeJSON(w, 400, map[string]string{"error": "缺少地址"})
		return
	}
	c, err := net.DialTimeout("tcp", req.Addr, 2*time.Second)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false})
		return
	}
	c.Close()
	writeJSON(w, 200, map[string]any{"ok": true})
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
