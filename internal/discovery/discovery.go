package discovery

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	beaconEvery = 5 * time.Second
	peerTimeout = 16 * time.Second
)

type Peer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Addr     string `json:"addr"`
	Online   bool   `json:"online"`
	Manual   bool   `json:"manual"`
	LastSeen int64  `json:"last_seen"`
}

type Registry struct {
	mu    sync.Mutex
	peers map[string]*Peer
}

func NewRegistry() *Registry {
	return &Registry{peers: map[string]*Peer{}}
}

func (r *Registry) Touch(id, name, addr string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.peers[id]
	if !ok {
		p = &Peer{ID: id}
		r.peers[id] = p
	}
	p.Name = name
	// 多网卡时同一信标会从多个地址到达，优先保留可路由的地址。
	if p.Addr == "" || !(linkLocal(addr) && !linkLocal(p.Addr)) {
		p.Addr = addr
	}
	p.LastSeen = time.Now().Unix()
	p.Online = true
}

func linkLocal(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast())
}

// Find 按完整/前缀 ID 或名称查找在线节点；前缀有多个匹配时不返回。
func (r *Registry) Find(q string) *Peer {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	low := strings.ToLower(q)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var hit *Peer
	n := 0
	for _, p := range r.peers {
		if p.ID == "" {
			continue
		}
		if now.Sub(time.Unix(p.LastSeen, 0)) >= peerTimeout && !p.Manual {
			continue
		}
		if strings.HasPrefix(strings.ToLower(p.ID), low) || strings.EqualFold(p.Name, q) {
			c := *p
			c.Online = true
			if strings.EqualFold(p.ID, low) || strings.EqualFold(p.Name, q) {
				return &c
			}
			hit = &c
			n++
		}
	}
	if n == 1 {
		return hit
	}
	return nil
}

// AddPeer 记录一个已解析出身份的节点（例如通过中继查到），重启前一直保留。
func (r *Registry) AddPeer(id, name, addr string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.peers[id]
	if !ok {
		p = &Peer{ID: id}
		r.peers[id] = p
	}
	p.Name = name
	if addr != "" {
		p.Addr = addr
	}
	p.Manual = true
	p.Online = true
	p.LastSeen = time.Now().Unix()
}

func (r *Registry) AddManual(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.peers {
		if p.Addr == addr {
			return
		}
	}
	key := "manual:" + addr
	if _, ok := r.peers[key]; !ok {
		r.peers[key] = &Peer{Addr: addr, Online: true, Manual: true, Name: addr, LastSeen: time.Now().Unix()}
	}
}

func (r *Registry) Get(id string) *Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.peers[id]; ok {
		c := *p
		c.Online = time.Since(time.Unix(c.LastSeen, 0)) < peerTimeout || c.Manual
		return &c
	}
	return nil
}

func (r *Registry) List() []Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := make([]Peer, 0, len(r.peers))
	for k, p := range r.peers {
		c := *p
		c.Online = now.Sub(time.Unix(c.LastSeen, 0)) < peerTimeout || c.Manual
		if !c.Online && !c.Manual {
			delete(r.peers, k)
			continue
		}
		out = append(out, c)
	}
	return out
}

type Beacon struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
	OS   string `json:"os"`
	TS   int64  `json:"ts"`
}

// Run 启动发现：一个组播接收循环 + 一个定时组播/广播发送循环。
func Run(ctx context.Context, port int, id, name string, p2pPort int, reg *Registry) {
	go listen(ctx, port, id, reg)
	send(ctx, port, id, name, p2pPort)
}

func listen(ctx context.Context, port int, selfID string, reg *Registry) {
	group := &net.UDPAddr{IP: net.ParseIP("239.255.72.83"), Port: port}
	c, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		log.Printf("[发现] 组播监听失败（可继续用手动地址/中继）: %v", err)
		return
	}
	defer c.Close()
	c.SetReadBuffer(1 << 20)
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	buf := make([]byte, 2048)
	for {
		n, src, err := c.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		var b Beacon
		if json.Unmarshal(buf[:n], &b) != nil || b.V != 1 || b.ID == "" || b.ID == selfID {
			continue
		}
		ip := src.IP
		if ip.IsUnspecified() {
			continue
		}
		reg.Touch(b.ID, b.Name, net.JoinHostPort(ip.String(), strconv.Itoa(b.Port)))
	}
}

func send(ctx context.Context, port int, id, name string, p2pPort int) {
	tick := time.NewTicker(beaconEvery)
	defer tick.Stop()
	beacon := func() []byte {
		b, _ := json.Marshal(Beacon{
			V: 1, ID: id, Name: name, Port: p2pPort,
			OS: runtime.GOOS, TS: time.Now().Unix(),
		})
		return b
	}
	sendAll(port, beacon())
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			sendAll(port, beacon())
		}
	}
}

// sendAll 从每个网卡分别发组播和定向广播；255.255.255.255 在部分系统上
// 必须绑定源地址才能发出，所以按网卡逐个发。
func sendAll(port int, data []byte) {
	targets := [][]byte{net.ParseIP("239.255.72.83").To4(), net.IPv4bcast.To4()}
	beacons := broadcastAddrs()
	for _, b := range beacons {
		targets = append(targets, b)
	}
	ifaces, _ := net.Interfaces()
	sent := false
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			src := ipnet.IP.To4()
			if src == nil {
				continue
			}
			for _, t := range targets {
				c, err := net.DialUDP("udp4", &net.UDPAddr{IP: src}, &net.UDPAddr{IP: t, Port: port})
				if err != nil {
					continue
				}
				c.Write(data)
				c.Close()
				sent = true
			}
		}
	}
	if !sent {
		for _, t := range targets[:2] {
			if c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: t, Port: port}); err == nil {
				c.Write(data)
				c.Close()
			}
		}
	}
}

func broadcastAddrs() []net.IP {
	var out []net.IP
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || len(ipnet.Mask) != 4 {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			b := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				b[i] = ip4[i] | ^ipnet.Mask[i]
			}
			out = append(out, b)
		}
	}
	return out
}
