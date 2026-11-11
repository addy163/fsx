package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"fsx/internal/discovery"
	"fsx/internal/httpsrv"
	"fsx/internal/identity"
	"fsx/internal/relay"
	"fsx/internal/store"
	"fsx/internal/tray"
	"fsx/internal/transfer"
)

const version = "0.2.0"

func main() {
	log.SetFlags(log.Ltime)
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "serve":
		cmdServe(args)
	case "send":
		cmdSend(args)
	case "ls":
		cmdLs(args)
	case "relay":
		cmdRelay(args)
	case "version":
		fmt.Println("fsx", version)
	case "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`fsx ` + version + ` — 局域网 / 跨网 P2P 文件传送

用法:
  fsx [serve] [选项]                    启动节点，浏览器打开控制台
  fsx send <节点ID|名称|IP:端口> <文件|目录>...   直接发送并显示进度
  fsx ls [选项]                         扫描局域网在线节点
  fsx relay [--listen :9270]            启动中继/信令服务

选项:
  --home <dir>         状态目录（默认用户配置目录下的 fsx）
  --name <name>        节点显示名（默认主机名）
  --p2p <port>         P2P 端口（默认 9272）
  --http <port>        控制台端口（默认 9271）
  --disc <port>        局域网发现端口（默认 9273）
  --relay <host:port>  中继地址，跨网传输需要
  --relay-fp <id>      固定中继身份指纹（默认首次连接时记录）
  --download <dir>     接收目录
  --no-discovery       关闭局域网发现
  --accept-all         自动接收所有传输（谨慎）
  --force-relay        始终经由中继（对称 NAT 场景）
  --no-browser         不自动打开浏览器
  --no-tray            关闭托盘客户端，保持命令行窗口（Windows）
`)
}

type commonOpts struct {
	home, name, download, relay, relayFP string
	p2p, http, disc                      int
	noDiscovery, acceptAll, noBrowser    bool
	forceRelay, noTray                   bool
}

func addCommon(fs *flag.FlagSet) *commonOpts {
	o := &commonOpts{}
	fs.StringVar(&o.home, "home", "", "状态目录")
	fs.StringVar(&o.name, "name", "", "节点显示名")
	fs.StringVar(&o.download, "download", "", "接收目录")
	fs.StringVar(&o.relay, "relay", "", "中继地址")
	fs.StringVar(&o.relayFP, "relay-fp", "", "中继身份指纹")
	fs.IntVar(&o.p2p, "p2p", 0, "P2P 端口")
	fs.IntVar(&o.http, "http", 0, "控制台端口")
	fs.IntVar(&o.disc, "disc", 0, "发现端口")
	fs.BoolVar(&o.noDiscovery, "no-discovery", false, "关闭局域网发现")
	fs.BoolVar(&o.acceptAll, "accept-all", false, "自动接收所有传输")
	fs.BoolVar(&o.noBrowser, "no-browser", false, "不打开浏览器")
	fs.BoolVar(&o.forceRelay, "force-relay", false, "始终经由中继传输")
	fs.BoolVar(&o.noTray, "no-tray", false, "关闭托盘客户端")
	return o
}

func (o *commonOpts) homeDir() string {
	if o.home != "" {
		return o.home
	}
	return defaultHome()
}

func defaultHome() string {
	if v := os.Getenv("FSX_HOME"); v != "" {
		return v
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return ".fsx"
	}
	return filepath.Join(d, "fsx")
}

func defaultDownload() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "fsx-downloads"
	}
	if fi, err := os.Stat(filepath.Join(h, "Downloads")); err == nil && fi.IsDir() {
		return filepath.Join(h, "Downloads", "fsx")
	}
	return filepath.Join(h, "fsx-downloads")
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "fsx"
	}
	return h
}

func applyOpts(st *store.Store, o *commonOpts) store.Config {
	cfg := st.Config()
	if o.name != "" {
		cfg.Name = o.name
	}
	if o.download != "" {
		cfg.DownloadDir = o.download
	}
	if o.p2p != 0 {
		cfg.P2PPort = o.p2p
	}
	if o.http != 0 {
		cfg.HTTPPort = o.http
	}
	if o.disc != 0 {
		cfg.DiscPort = o.disc
	}
	if o.relay != "" {
		cfg.Relay = o.relay
	}
	if o.relayFP != "" {
		cfg.RelayFP = o.relayFP
	}
	os.MkdirAll(cfg.DownloadDir, 0o755)
	st.Update(func(c *store.Config) { *c = cfg })
	return cfg
}

func pairCode() string {
	b := make([]byte, 4)
	rand.Read(b)
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return fmt.Sprintf("%06d", n%1000000)
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	o := addCommon(fs)
	fs.Parse(args)

	home := o.homeDir()
	useTray := tray.Supported() && !o.noTray
	if useTray {
		tray.HideConsole()
		if closeLog, err := tray.SetupLog(home); err == nil {
			defer closeLog()
		}
	}
	fatal := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Print(msg)
		if useTray {
			tray.Error("fsx 启动失败", msg)
		}
		os.Exit(1)
	}

	id, err := identity.Load(home)
	if err != nil {
		fatal("读取身份失败: %v", err)
	}
	st, err := store.Open(home, store.Config{
		Name: hostname(), DownloadDir: defaultDownload(),
		HTTPPort: 9271, P2PPort: 9272, DiscPort: 9273,
	})
	if err != nil {
		fatal("读取配置失败: %v", err)
	}
	cfg := applyOpts(st, o)

	// 托盘模式下重复双击 exe：直接把已在运行的节点控制台打开
	if useTray {
		if port, ok := runningInstance(cfg.HTTPPort); ok {
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
			return
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, p2pPort, err := listenPort(cfg.P2PPort)
	if err != nil {
		fatal("P2P 端口不可用: %v", err)
	}
	if p2pPort != cfg.P2PPort {
		cfg.P2PPort = p2pPort
		st.Update(func(c *store.Config) { c.P2PPort = p2pPort })
	}

	reg := discovery.NewRegistry()
	inj := transfer.NewInjector()

	var wan transfer.WAN
	if cfg.Relay != "" {
		rc := relay.New(cfg.Relay, id, cfg.Name, cfg.RelayFP,
			func(fp string) {
				log.Printf("[中继] 首次连接，已记录指纹 %s", fp)
				st.SetRelayFP(fp)
			},
			func(c net.Conn) { inj.Push(c) })
		rc.P2PPort = p2pPort
		wan = rc
		go rc.Run(ctx)
	}

	mgr := transfer.New(id, reg, st, wan, p2pPort)
	mgr.SetAcceptAll(o.acceptAll)
	mgr.SetForceRelay(o.forceRelay)
	go mgr.ServeListener(ln)
	go inj.Serve(mgr)

	if !o.noDiscovery {
		go discovery.Run(ctx, cfg.DiscPort, id.ID, cfg.Name, p2pPort, reg)
	}

	code := pairCode()
	httpLn, httpPort, err := listenPort(cfg.HTTPPort)
	if err != nil {
		fatal("控制台端口不可用: %v", err)
	}
	if httpPort != cfg.HTTPPort {
		cfg.HTTPPort = httpPort
		st.Update(func(c *store.Config) { c.HTTPPort = httpPort })
	}
	hs := httpsrv.New(mgr, st, id, code, version, httpPort, p2pPort)
	srv := &http.Server{Handler: hs.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			log.Printf("控制台退出: %v", err)
		}
	}()

	lan := lanIP()
	fmt.Printf("\nfsx %s · %s\n", version, cfg.Name)
	fmt.Printf("  节点 ID    %s\n", identity.Short(id.ID))
	fmt.Printf("  P2P        :%d\n", p2pPort)
	fmt.Printf("  本机控制台 http://127.0.0.1:%d\n", httpPort)
	if lan != "" {
		fmt.Printf("  局域网     http://%s:%d\n", lan, httpPort)
	}
	fmt.Printf("  配对码     %s   （手机等设备首次打开时输入）\n", code)
	if cfg.Relay != "" {
		fmt.Printf("  中继       %s\n", cfg.Relay)
	} else {
		fmt.Printf("  中继       未配置（仅局域网可用）\n")
	}
	fmt.Printf("  接收目录   %s\n\n", cfg.DownloadDir)

	url := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	if useTray {
		log.Printf("已启动 · 节点 %s · 控制台 %s · 配对码 %s", identity.Short(id.ID), url, code)
		go func() {
			<-ctx.Done()
			tray.Quit()
		}()
		if err := tray.Run(tray.Options{
			Name: cfg.Name, URL: url, PairCode: code, NodeID: id.ID,
		}); err != nil {
			log.Printf("托盘客户端退出: %v", err)
			tray.ShowConsole()
			openBrowser(url)
			<-ctx.Done()
		}
		stop()
	} else {
		if !o.noBrowser {
			openBrowser(url)
		}
		<-ctx.Done()
	}
	log.Println("正在退出…")
	shctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shctx)
}

func cmdSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	o := addCommon(fs)
	peerFlag := fs.String("peer", "", "节点 ID 或 IP:端口")
	fs.Parse(args)
	rest := fs.Args()
	peerArg := *peerFlag
	if peerArg == "" && len(rest) > 0 {
		peerArg = rest[0]
		rest = rest[1:]
	}
	if peerArg == "" || len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "用法: fsx send <节点ID|名称|IP:端口> <文件|目录>...")
		os.Exit(2)
	}

	home := o.homeDir()
	id, err := identity.Load(home)
	if err != nil {
		log.Fatalf("读取身份失败: %v", err)
	}
	st, err := store.Open(home, store.Config{
		Name: hostname(), DownloadDir: defaultDownload(),
		HTTPPort: 9271, P2PPort: 9272, DiscPort: 9273,
	})
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	cfg := applyOpts(st, o)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := discovery.NewRegistry()
	var wan transfer.WAN
	if cfg.Relay != "" {
		rc := relay.New(cfg.Relay, id, cfg.Name, cfg.RelayFP,
			func(fp string) { st.SetRelayFP(fp) },
			func(c net.Conn) { c.Close() })
		rc.P2PPort = cfg.P2PPort
		wan = rc
		go rc.Run(ctx)
	}
	if !o.noDiscovery {
		go discovery.Run(ctx, cfg.DiscPort, id.ID, cfg.Name, cfg.P2PPort, reg)
	}

	mgr := transfer.New(id, reg, st, wan, cfg.P2PPort)
	mgr.SetForceRelay(o.forceRelay)

	var peerID, addr string
	if strings.Contains(peerArg, ":") {
		addr = peerArg
	} else {
		peerID = peerArg
		fmt.Print("扫描节点")
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if p := reg.Find(peerArg); p != nil && p.Addr != "" {
				peerID, addr = p.ID, p.Addr
				break
			}
			fmt.Print(".")
			time.Sleep(300 * time.Millisecond)
		}
		fmt.Print("\r")
		if addr == "" {
			if pid, ad, _, err := mgr.Resolve(peerArg); err == nil {
				peerID, addr = pid, ad
				fmt.Printf("局域网内未发现，改走中继连接 %s\n", identity.Short(peerID))
			} else {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		}
	}

	jobID, err := mgr.SendTo(peerID, addr, rest, nil)
	if err != nil {
		log.Fatalf("发送失败: %v", err)
	}
	tty := isTTY()
	last := int64(-1)
	for {
		j := mgr.FindJob(jobID)
		if j == nil {
			return
		}
		switch j.State {
		case "done":
			fmt.Printf("\r%s已完成  %s  %s\n", clearLine(tty), fmtBytes(j.Total), j.Mode)
			return
		case "failed", "rejected", "canceled":
			fmt.Fprintf(os.Stderr, "\r%s%s: %s\n", clearLine(tty), stateLabel(j.State), j.Error)
			os.Exit(1)
		}
		if j.Done != last {
			fmt.Printf("\r%s / %s  %s/s        ", fmtBytes(j.Done), fmtBytes(j.Total), fmtBytes(j.Speed))
			if tty {
				// 覆盖上一次更长的进度行
				fmt.Print("          ")
			}
			last = j.Done
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func stateLabel(s string) string {
	switch s {
	case "failed":
		return "传输失败"
	case "rejected":
		return "对方拒绝"
	case "canceled":
		return "已取消"
	}
	return s
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func clearLine(tty bool) string {
	if tty {
		return "\x1b[K"
	}
	return ""
}

func cmdLs(args []string) {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	o := addCommon(fs)
	fs.Parse(args)

	home := o.homeDir()
	id, err := identity.Load(home)
	if err != nil {
		log.Fatalf("读取身份失败: %v", err)
	}
	st, err := store.Open(home, store.Config{
		Name: hostname(), DownloadDir: defaultDownload(),
		HTTPPort: 9271, P2PPort: 9272, DiscPort: 9273,
	})
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	cfg := applyOpts(st, o)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reg := discovery.NewRegistry()
	go discovery.Run(ctx, cfg.DiscPort, id.ID, cfg.Name, cfg.P2PPort, reg)
	fmt.Println("扫描 3 秒…")
	time.Sleep(3 * time.Second)
	peers := reg.List()
	if len(peers) == 0 {
		fmt.Println("没有发现节点")
		return
	}
	fmt.Printf("%-12s %-20s %-24s %s\n", "ID", "名称", "地址", "状态")
	for _, p := range peers {
		state := "离线"
		if p.Online {
			state = "在线"
		}
		fmt.Printf("%-12s %-20s %-24s %s\n", identity.Short(p.ID), p.Name, p.Addr, state)
	}
}

func cmdRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	home := fs.String("home", "", "状态目录")
	listen := fs.String("listen", ":9270", "监听地址")
	fs.Parse(args)

	dir := *home
	if dir == "" {
		dir = filepath.Join(defaultHome(), "relay")
	}
	id, err := identity.Load(dir)
	if err != nil {
		log.Fatalf("读取身份失败: %v", err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := relay.NewServer(id)
	fmt.Printf("\nfsx 中继 · %s\n", *listen)
	fmt.Printf("  指纹       %s\n", id.ID)
	fmt.Printf("  客户端配置  --relay <公网IP>:%s\n\n", portOf(*listen))
	go srv.Serve(ln)
	<-ctx.Done()
	ln.Close()
}

func portOf(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "9270"
	}
	return p
}

func listenPort(base int) (net.Listener, int, error) {
	var lastErr error
	for i := 0; i < 20; i++ {
		p := base + i
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

// runningInstance 在候选端口里找已在运行的 fsx 控制台，返回其端口。
func runningInstance(base int) (int, bool) {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	for port := base; port < base+5; port++ {
		resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state", port))
		if err != nil {
			continue
		}
		ok := resp.Header.Get("X-FSX-Server") == "fsx"
		resp.Body.Close()
		if ok {
			return port, true
		}
	}
	return 0, false
}

func lanIP() string {
	if c, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
					return ip4.String()
				}
			}
		}
	}
	return ""
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

func fmtBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	u := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	v := float64(n)
	for v >= 1024 && i < len(u)-1 {
		v /= 1024
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, u[i])
	}
	return fmt.Sprintf("%.1f %s", v, u[i])
}
