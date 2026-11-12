package relay

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"fsx/internal/addrs"
	"fsx/internal/identity"
	"fsx/internal/proto"
)

const (
	RRegister   byte = 1
	RRegistered byte = 2
	RLookup     byte = 3
	RFound      byte = 4
	RNotFound   byte = 5
	ROpen       byte = 6
	RIncoming   byte = 7
	ROpened     byte = 8
	ROpenFailed byte = 9
	RData       byte = 10
	RClose      byte = 11
	RPing       byte = 12
	RPong       byte = 13
)

type Register struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	P2PPort int      `json:"p2p_port"`
	V6      []string `json:"v6,omitempty"`
}

type Registered struct {
	Observed string `json:"observed"`
	Direct   string `json:"direct"`
	Err      string `json:"err,omitempty"`
}

type Lookup struct {
	ID string `json:"id"`
}

type Found struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Direct string   `json:"direct"`
	V6     []string `json:"v6,omitempty"`
	Q      string   `json:"q,omitempty"` // 原始查询串，短 ID 查询时用于回调匹配
}

type Open struct {
	SID    string `json:"sid"`
	Target string `json:"target"`
}

type Incoming struct {
	SID      string `json:"sid"`
	FromID   string `json:"from_id"`
	FromName string `json:"from_name"`
}

type Opened struct {
	SID string `json:"sid"`
	Err string `json:"err,omitempty"`
}

type Close struct {
	SID string `json:"sid"`
}

// Client 维持一条到中继的常驻 TLS 连接，提供地址查询与按流转发。
type Client struct {
	addr     string
	name     string
	id       *identity.Identity
	fp       string
	onFP     func(string)
	onStream func(net.Conn)
	P2PPort  int

	mu       sync.Mutex
	pc       *proto.Conn
	raw      net.Conn
	streams  map[string]*Stream
	openWait map[string]chan error
	lookups  map[string]chan Found
	cache    map[string]cached
	status   string
	lastPong time.Time
}

type cached struct {
	info Found
	at   time.Time
}

func New(addr string, id *identity.Identity, name, fp string, onFP func(string), onStream func(net.Conn)) *Client {
	return &Client{
		addr: addr, id: id, name: name, fp: fp, onFP: onFP, onStream: onStream,
		streams: map[string]*Stream{}, openWait: map[string]chan error{},
		lookups: map[string]chan Found{}, cache: map[string]cached{},
		status: "connecting",
	}
}

func (c *Client) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) setStatus(s string) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
}

func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.connect(ctx)
		if err != nil {
			c.setStatus("offline")
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		c.setStatus("online")
		go c.pingLoop()
		c.readLoop()
		c.teardown()
		if ctx.Err() != nil {
			return
		}
		c.setStatus("offline")
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) connect(ctx context.Context) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return err
	}
	tc := tls.Client(raw, &tls.Config{
		Certificates:       []tls.Certificate{c.id.Cert},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		raw.Close()
		return err
	}
	relayID, err := identity.PeerID(tc.ConnectionState())
	if err != nil {
		tc.Close()
		return err
	}
	if c.fp != "" && c.fp != relayID {
		tc.Close()
		return fmt.Errorf("中继身份不匹配: 配置 %s 实际 %s", c.fp, relayID)
	}
	if c.fp == "" && c.onFP != nil {
		c.onFP(relayID)
	}
	pc := proto.New(tc)
	if err := pc.SendJSON(RRegister, Register{ID: c.id.ID, Name: c.name, P2PPort: c.P2PPort, V6: addrs.GlobalV6()}); err != nil {
		tc.Close()
		return err
	}
	var reg Registered
	if err := pc.RecvJSON(RRegistered, &reg); err != nil {
		tc.Close()
		return err
	}
	if reg.Err != "" {
		tc.Close()
		return errors.New(reg.Err)
	}
	c.mu.Lock()
	c.pc = pc
	c.raw = tc
	c.lastPong = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *Client) pingLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		c.mu.Lock()
		pc, last := c.pc, c.lastPong
		c.mu.Unlock()
		if pc == nil {
			return
		}
		if time.Since(last) > 90*time.Second {
			pc.Close()
			return
		}
		if err := pc.SendJSON(RPing, struct{}{}); err != nil {
			pc.Close()
			return
		}
	}
}

func (c *Client) readLoop() {
	c.mu.Lock()
	pc := c.pc
	c.mu.Unlock()
	if pc == nil {
		return
	}
	for {
		t, n, err := pc.Next()
		if err != nil {
			return
		}
		switch t {
		case RData:
			var sidb [8]byte
			if err := pc.ReadBody(sidb[:]); err != nil {
				return
			}
			pl := n - 8
			if pl < 0 || pl > proto.MaxBody {
				return
			}
			buf := make([]byte, pl)
			if err := pc.ReadBody(buf); err != nil {
				return
			}
			sid := hex.EncodeToString(sidb[:])
			c.mu.Lock()
			s := c.streams[sid]
			c.mu.Unlock()
			if s != nil {
				s.push(buf)
			}

		case RIncoming:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
			var in Incoming
			if json.Unmarshal(b, &in) != nil || in.SID == "" {
				continue
			}
			s := newStream(c, in.SID)
			c.mu.Lock()
			c.streams[in.SID] = s
			c.mu.Unlock()
			if c.onStream != nil {
				go c.onStream(s)
			}

		case ROpened, ROpenFailed:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
			var op Opened
			json.Unmarshal(b, &op)
			c.mu.Lock()
			ch := c.openWait[op.SID]
			delete(c.openWait, op.SID)
			if t == ROpenFailed {
				delete(c.streams, op.SID)
			}
			c.mu.Unlock()
			if ch != nil {
				var e error
				if t == ROpenFailed {
					e = errors.New(defaultStr(op.Err, "对方未连接中继"))
				}
				ch <- e
			}

		case RFound, RNotFound:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
			var f Found
			if t == RFound {
				json.Unmarshal(b, &f)
			} else {
				json.Unmarshal(b, &f)
				f.Direct = ""
			}
			c.mu.Lock()
			key := f.ID
			if f.Q != "" {
				key = f.Q
			}
			ch := c.lookups[key]
			delete(c.lookups, key)
			if t == RFound && f.Direct != "" {
				c.cache[f.ID] = cached{info: f, at: time.Now()}
			}
			c.mu.Unlock()
			if ch != nil {
				ch <- f
			}

		case RClose:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
			var cl Close
			json.Unmarshal(b, &cl)
			c.mu.Lock()
			s := c.streams[cl.SID]
			delete(c.streams, cl.SID)
			c.mu.Unlock()
			if s != nil {
				s.closeLocal()
			}

		case RPong:
			if n > 0 {
				b := make([]byte, n)
				pc.ReadBody(b)
			}
			c.mu.Lock()
			c.lastPong = time.Now()
			c.mu.Unlock()

		default:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
		}
	}
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (c *Client) teardown() {
	c.mu.Lock()
	if c.raw != nil {
		c.raw.Close()
	}
	streams := c.streams
	opens := c.openWait
	lookups := c.lookups
	c.streams = map[string]*Stream{}
	c.openWait = map[string]chan error{}
	c.lookups = map[string]chan Found{}
	c.pc = nil
	c.raw = nil
	c.mu.Unlock()
	for _, s := range streams {
		s.closeLocal()
	}
	for _, ch := range opens {
		ch <- errors.New("中继连接断开")
	}
	for _, ch := range lookups {
		ch <- Found{}
	}
}

// Lookup 向中继查询节点，id 可以是完整 ID 或唯一前缀。
func (c *Client) Lookup(id string) (Found, bool) {
	if id == "" {
		return Found{}, false
	}
	c.mu.Lock()
	if e, ok := c.cache[id]; ok && time.Since(e.at) < 20*time.Second {
		c.mu.Unlock()
		return e.info, true
	}
	c.mu.Unlock()
	pc, err := c.waitConn(context.Background(), 3*time.Second)
	if err != nil {
		return Found{}, false
	}
	c.mu.Lock()
	ch := make(chan Found, 1)
	c.lookups[id] = ch
	c.mu.Unlock()
	if err := pc.SendJSON(RLookup, Lookup{ID: id}); err != nil {
		c.mu.Lock()
		delete(c.lookups, id)
		c.mu.Unlock()
		return Found{}, false
	}
	select {
	case f := <-ch:
		if f.ID == "" {
			return Found{}, false
		}
		if f.Direct != "" {
			c.mu.Lock()
			c.cache[f.ID] = cached{info: f, at: time.Now()}
			c.mu.Unlock()
		}
		return f, true
	case <-time.After(4 * time.Second):
		c.mu.Lock()
		delete(c.lookups, id)
		c.mu.Unlock()
		return Found{}, false
	}
}

// Resolve 通过中继把完整/前缀 ID 解析成节点身份与直连提示。
func (c *Client) Resolve(id string) (peerID, name, direct string, ok bool) {
	f, ok := c.Lookup(id)
	if !ok {
		return "", "", "", false
	}
	return f.ID, f.Name, f.Direct, true
}

// PublicAddr 返回对方的直连提示地址（公网 IP + 对方 P2P 端口）。
// 对方在 NAT 后面时会连不通，由调用方回落到中继转发。
func (c *Client) PublicAddr(peerID string) (string, bool) {
	f, ok := c.Lookup(peerID)
	if !ok || f.Direct == "" {
		return "", false
	}
	return f.Direct, true
}

// PublicV6 返回对方通告的全局 IPv6 地址，供跨网直连尝试。
func (c *Client) PublicV6(peerID string) ([]string, bool) {
	f, ok := c.Lookup(peerID)
	if !ok || len(f.V6) == 0 {
		return nil, false
	}
	return f.V6, true
}

func (c *Client) waitConn(ctx context.Context, d time.Duration) (*proto.Conn, error) {
	deadline := time.Now().Add(d)
	for {
		c.mu.Lock()
		pc := c.pc
		c.mu.Unlock()
		if pc != nil {
			return pc, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("未连接中继")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (c *Client) OpenRelay(ctx context.Context, peerID string) (net.Conn, error) {
	pc, err := c.waitConn(ctx, 8*time.Second)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	sid := randHex(8)
	s := newStream(c, sid)
	ch := make(chan error, 1)
	c.streams[sid] = s
	c.openWait[sid] = ch
	c.mu.Unlock()

	if err := pc.SendJSON(ROpen, Open{SID: sid, Target: peerID}); err != nil {
		c.drop(sid)
		return nil, err
	}
	select {
	case err := <-ch:
		if err != nil {
			c.drop(sid)
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		c.drop(sid)
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		c.drop(sid)
		return nil, errors.New("中继连接超时")
	}
}

func (c *Client) drop(sid string) {
	c.mu.Lock()
	delete(c.streams, sid)
	delete(c.openWait, sid)
	c.mu.Unlock()
}

func (c *Client) sendData(sid string, p []byte) error {
	c.mu.Lock()
	pc := c.pc
	c.mu.Unlock()
	if pc == nil {
		return errors.New("未连接中继")
	}
	sidb, err := hex.DecodeString(sid)
	if err != nil {
		return err
	}
	body := make([]byte, 8+len(p))
	copy(body, sidb)
	copy(body[8:], p)
	return pc.SendRaw(RData, body)
}

func (c *Client) sendClose(sid string) {
	c.mu.Lock()
	pc := c.pc
	delete(c.streams, sid)
	c.mu.Unlock()
	if pc != nil {
		pc.SendJSON(RClose, Close{SID: sid})
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type Stream struct {
	c    *Client
	sid  string
	ch   chan []byte
	buf  []byte
	done chan struct{}
	once sync.Once

	mu        sync.Mutex
	deadline  time.Time
	closeOnce sync.Once
}

func newStream(c *Client, sid string) *Stream {
	return &Stream{c: c, sid: sid, ch: make(chan []byte, 128), done: make(chan struct{})}
}

func (s *Stream) push(b []byte) {
	select {
	case s.ch <- b:
	case <-s.done:
	}
}

func (s *Stream) closeLocal() {
	s.closeOnce.Do(func() { close(s.done) })
}

func (s *Stream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			s.mu.Unlock()
			return n, nil
		}
		dl := s.deadline
		s.mu.Unlock()

		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			t := time.NewTimer(d)
			defer t.Stop()
			timeout = t.C
		}
		select {
		case b, ok := <-s.ch:
			if !ok {
				return 0, io.EOF
			}
			s.mu.Lock()
			s.buf = b
			s.mu.Unlock()
		case <-s.done:
			return 0, io.EOF
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

func (s *Stream) Write(p []byte) (int, error) {
	select {
	case <-s.done:
		return 0, io.ErrClosedPipe
	default:
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > 32<<10 {
			n = 32 << 10
		}
		if err := s.c.sendData(s.sid, p[:n]); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (s *Stream) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	s.c.sendClose(s.sid)
	return nil
}

func (s *Stream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.deadline = t
	s.mu.Unlock()
	return nil
}

func (s *Stream) SetReadDeadline(t time.Time) error  { return s.SetDeadline(t) }
func (s *Stream) SetWriteDeadline(t time.Time) error { return nil }

type streamAddr struct{}

func (streamAddr) Network() string { return "relay" }
func (streamAddr) String() string  { return "relay" }

func (s *Stream) LocalAddr() net.Addr  { return streamAddr{} }
func (s *Stream) RemoteAddr() net.Addr { return streamAddr{} }
