package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"fsx/internal/discovery"
	"fsx/internal/identity"
	"fsx/internal/proto"
	"fsx/internal/store"
)

var errCanceled = errors.New("已取消")

// WAN 由中继客户端实现；没有配置中继时为 nil。
type WAN interface {
	PublicAddr(peerID string) (string, bool)
	OpenRelay(ctx context.Context, peerID string) (net.Conn, error)
	Resolve(id string) (peerID, name, direct string, ok bool)
	Status() string
}

type Item struct {
	ID    uint32 `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Done  int64  `json:"done"`
	MTime int64  `json:"mtime"`

	SrcPath   string `json:"-"`
	FinalPath string `json:"-"`
	PartPath  string `json:"-"`
	Hd        string `json:"-"`
	Tl        string `json:"-"`
	Offset    int64  `json:"-"`
	Finalized bool   `json:"-"`
}

type Message struct {
	FromID   string `json:"from_id"`
	FromName string `json:"from_name"`
	Body     string `json:"body"`
	Time     int64  `json:"time"`
	Dir      string `json:"dir"`
}

type Job struct {
	ID       string  `json:"id"`
	Dir      string  `json:"dir"` // send | recv
	PeerID   string  `json:"peer_id"`
	PeerName string  `json:"peer_name"`
	Addr     string  `json:"addr"`
	Mode     string  `json:"mode"`
	State    string  `json:"state"`
	Error    string  `json:"error"`
	SAS      string  `json:"sas"`
	Items    []*Item `json:"items"`
	Total    int64   `json:"total"`
	Done     int64   `json:"done"`
	Speed    int64   `json:"speed"`
	Created  int64   `json:"created"`
	Updated  int64   `json:"updated"`

	ctx      context.Context
	cancelFn context.CancelFunc
	decide   chan bool
	pc       *proto.Conn
	cleanup  func()

	lastDone int64
	lastAt   time.Time
}

func (j *Job) clone() *Job {
	c := *j
	c.Items = make([]*Item, len(j.Items))
	for i, it := range j.Items {
		v := *it
		c.Items[i] = &v
	}
	return &c
}

type Snapshot struct {
	Peers    []discovery.Peer `json:"peers"`
	Jobs     []*Job           `json:"jobs"`
	Messages []Message        `json:"messages"`
	Relay    string           `json:"relay"`
}

type Manager struct {
	self *identity.Identity
	reg  *discovery.Registry
	st   *store.Store
	wan  WAN
	port int

	serverTLS *tls.Config
	clientTLS *tls.Config

	mu         sync.Mutex
	jobs       map[string]*Job
	order      []string
	msgs       []Message
	acceptAll  bool
	forceRelay bool
}

func New(self *identity.Identity, reg *discovery.Registry, st *store.Store, wan WAN, port int) *Manager {
	return &Manager{
		self:      self,
		reg:       reg,
		st:        st,
		wan:       wan,
		port:      port,
		serverTLS: self.ServerConfig(),
		clientTLS: self.ClientConfig(),
		jobs:      map[string]*Job{},
	}
}

func (m *Manager) Self() *identity.Identity { return m.self }

func (m *Manager) AddManual(addr string) { m.reg.AddManual(addr) }

func (m *Manager) AddPeer(id, name, addr string) { m.reg.AddPeer(id, name, addr) }

// Resolve 把「IP:端口」「节点 ID（可只用前 8 位）」或节点名解析成可达目标。
// ID 先查局域网发现，查不到再问中继；两者都没有则无法定位。
func (m *Manager) Resolve(q string) (peerID, addr, name string, err error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", "", "", errors.New("请输入节点 ID 或 IP:端口")
	}
	if host, port, e := net.SplitHostPort(q); e == nil && host != "" && port != "" {
		return "", q, "", nil
	}
	if p := m.reg.Find(q); p != nil {
		return p.ID, p.Addr, p.Name, nil
	}
	if m.wan != nil {
		if pid, nm, direct, ok := m.wan.Resolve(q); ok {
			return pid, direct, nm, nil
		}
	}
	if m.wan == nil {
		return "", "", "", fmt.Errorf("没找到节点 %q：不在同一局域网且未配置中继，也可以直接填 IP:端口", q)
	}
	return "", "", "", fmt.Errorf("没找到节点 %q：请确认对方在线并已连接同一中继", q)
}

func (m *Manager) SetAcceptAll(v bool) {
	m.mu.Lock()
	m.acceptAll = v
	m.mu.Unlock()
}

// SetForceRelay 跳过局域网/直连尝试，始终经由中继转发（对称 NAT 场景）。
func (m *Manager) SetForceRelay(v bool) {
	m.mu.Lock()
	m.forceRelay = v
	m.mu.Unlock()
}

func (m *Manager) autoAccept(peerID string) bool {
	m.mu.Lock()
	force := m.acceptAll
	m.mu.Unlock()
	return force || m.st.AutoAccept(peerID)
}

func (m *Manager) hello() proto.Hello {
	return proto.Hello{V: proto.Version, ID: m.self.ID, Name: m.st.Config().Name, OS: goos(), Port: m.port}
}

type Injector struct {
	ch chan net.Conn
}

func NewInjector() *Injector { return &Injector{ch: make(chan net.Conn, 32)} }

func (i *Injector) Push(c net.Conn) { i.ch <- c }

func (i *Injector) Serve(m *Manager) {
	for c := range i.ch {
		go m.HandleConn(c)
	}
}

func (m *Manager) ServeListener(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go m.HandleConn(c)
	}
}

// HandleConn 处理入站 P2P 连接：TLS 握手 → 交换 HELLO → 收 OFFER/TEXT。
func (m *Manager) HandleConn(raw net.Conn) {
	defer raw.Close()
	tc := tls.Server(raw, m.serverTLS)
	tc.SetDeadline(time.Now().Add(20 * time.Second))
	if err := tc.Handshake(); err != nil {
		return
	}
	cs := tc.ConnectionState()
	peerID, err := identity.PeerID(cs)
	if err != nil {
		return
	}
	pc := proto.New(tc)
	var h proto.Hello
	if err := pc.RecvJSON(proto.THello, &h); err != nil {
		return
	}
	if h.ID != peerID || h.V != proto.Version {
		return
	}
	if err := pc.SendJSON(proto.THello, m.hello()); err != nil {
		return
	}
	if host, _, err := net.SplitHostPort(raw.RemoteAddr().String()); err == nil && h.Port > 0 {
		m.reg.Touch(peerID, h.Name, net.JoinHostPort(host, strconv.Itoa(h.Port)))
	}
	sas := identity.SAS(cs, m.self.ID, peerID)
	tc.SetDeadline(time.Time{})

	t, n, err := pc.Next()
	if err != nil {
		return
	}
	switch t {
	case proto.TOffer:
		m.recvOffer(pc, peerID, h.Name, sas, n)
	case proto.TText:
		m.recvText(pc, peerID, h.Name, n)
	default:
		pc.SendJSON(proto.TResult, proto.Result{OK: false, Err: "不支持的帧"})
	}
}

type session struct {
	pc     *proto.Conn
	peerID string
	name   string
	sas    string
	mode   string
}

func (m *Manager) secureDial(ctx context.Context, addr, expectID string) (*session, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return m.tlsHandshake(ctx, raw, expectID)
}

func (m *Manager) tlsHandshake(ctx context.Context, raw net.Conn, expectID string) (*session, error) {
	tc := tls.Client(raw, m.clientTLS)
	tc.SetDeadline(time.Now().Add(20 * time.Second))
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	cs := tc.ConnectionState()
	peerID, err := identity.PeerID(cs)
	if err != nil {
		tc.Close()
		return nil, err
	}
	if expectID != "" && peerID != expectID {
		tc.Close()
		return nil, errors.New("对端身份与预期不符")
	}
	pc := proto.New(tc)
	if err := pc.SendJSON(proto.THello, m.hello()); err != nil {
		tc.Close()
		return nil, err
	}
	var h proto.Hello
	if err := pc.RecvJSON(proto.THello, &h); err != nil {
		tc.Close()
		return nil, err
	}
	if h.ID != peerID {
		tc.Close()
		return nil, errors.New("HELLO 与证书身份不一致")
	}
	tc.SetDeadline(time.Time{})
	return &session{pc: pc, peerID: peerID, name: h.Name, sas: identity.SAS(cs, m.self.ID, peerID)}, nil
}

// dialPeer 依次尝试：调用方给的地址 → 局域网发现地址 → 中继查到的公网地址 → 中继转发。
func (m *Manager) dialPeer(ctx context.Context, peerID, addr string) (*session, error) {
	m.mu.Lock()
	forceRelay := m.forceRelay
	m.mu.Unlock()
	if forceRelay {
		if m.wan == nil {
			return nil, errors.New("已设为强制中继，但未配置中继地址")
		}
		if peerID == "" {
			return nil, errors.New("强制中继需要对方完整节点 ID")
		}
		rc, err := m.wan.OpenRelay(ctx, peerID)
		if err != nil {
			return nil, err
		}
		s, err := m.tlsHandshake(ctx, rc, peerID)
		if err != nil {
			rc.Close()
			return nil, err
		}
		s.mode = "relay"
		return s, nil
	}
	type cand struct{ addr, mode string }
	var cands []cand
	seen := map[string]bool{}
	add := func(a, mode string) {
		if a == "" || seen[a] {
			return
		}
		seen[a] = true
		cands = append(cands, cand{a, mode})
	}
	add(addr, "lan")
	if p := m.reg.Get(peerID); p != nil {
		add(p.Addr, "lan")
	}
	if m.wan != nil && peerID != "" {
		if a, ok := m.wan.PublicAddr(peerID); ok {
			add(a, "direct")
		}
	}
	var lastErr error
	for _, c := range cands {
		s, err := m.secureDial(ctx, c.addr, peerID)
		if err == nil {
			s.mode = c.mode
			return s, nil
		}
		lastErr = err
	}
	if m.wan != nil && peerID != "" {
		rc, err := m.wan.OpenRelay(ctx, peerID)
		if err == nil {
			s, err := m.tlsHandshake(ctx, rc, peerID)
			if err == nil {
				s.mode = "relay"
				return s, nil
			}
			rc.Close()
			lastErr = err
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("找不到该节点，请确认对方在线")
	}
	return nil, lastErr
}

func (m *Manager) SendTo(peerID, addr string, paths []string, cleanup func()) (string, error) {
	items, total, err := buildItems(paths)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", errors.New("没有可发送的文件")
	}
	now := time.Now().Unix()
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		ID: newID(), Dir: "send", PeerID: peerID, Addr: addr,
		State: "connecting", Items: items, Total: total,
		Created: now, Updated: now, cleanup: cleanup,
		ctx: ctx, cancelFn: cancel, lastAt: time.Now(),
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()
	go m.runSend(j, peerID, addr)
	return j.ID, nil
}

func (m *Manager) runSend(j *Job, peerID, addr string) {
	defer j.cancelFn()
	s, err := m.dialPeer(j.ctx, peerID, addr)
	if err != nil {
		m.failJob(j, err)
		return
	}
	defer s.pc.Close()
	m.mu.Lock()
	j.pc = s.pc
	j.PeerID = s.peerID
	j.PeerName = s.name
	j.Mode = s.mode
	j.SAS = s.sas
	j.State = "waiting"
	j.Updated = time.Now().Unix()
	m.mu.Unlock()

	offer := proto.Offer{SID: j.ID}
	for _, it := range j.Items {
		offer.Items = append(offer.Items, proto.Item{
			ID: it.ID, Name: it.Name, Size: it.Size, MTime: it.MTime, Head: it.Hd, Tail: it.Tl,
		})
	}
	if err := s.pc.SendJSON(proto.TOffer, offer); err != nil {
		m.failJob(j, err)
		return
	}

	s.pc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	t, n, err := s.pc.Next()
	if err != nil {
		m.failJob(j, err)
		return
	}
	if t == proto.TReject {
		b := make([]byte, n)
		s.pc.ReadBody(b)
		var r proto.Reject
		json.Unmarshal(b, &r)
		msg := r.Reason
		if msg == "" {
			msg = "对方拒绝接收"
		}
		m.setState(j, "rejected", msg)
		return
	}
	if t != proto.TAccept {
		m.failJob(j, fmt.Errorf("意外的应答帧 %d", t))
		return
	}
	b := make([]byte, n)
	if err := s.pc.ReadBody(b); err != nil {
		m.failJob(j, err)
		return
	}
	var acc proto.Accept
	if err := json.Unmarshal(b, &acc); err != nil {
		m.failJob(j, err)
		return
	}
	s.pc.SetReadDeadline(time.Time{})

	m.mu.Lock()
	for _, it := range j.Items {
		if off, ok := acc.Offsets[it.ID]; ok && off >= 0 && off <= it.Size {
			it.Offset = off
			it.Done = off
		}
	}
	j.State = "active"
	j.Updated = time.Now().Unix()
	m.mu.Unlock()

	buf := make([]byte, proto.ChunkSize)
	for _, it := range j.Items {
		if err := m.sendFile(j, s.pc, it, buf); err != nil {
			if errors.Is(err, errCanceled) || j.ctx.Err() != nil {
				return
			}
			m.failJob(j, err)
			s.pc.SendJSON(proto.TCancel, proto.Cancel{SID: j.ID, Reason: err.Error()})
			return
		}
		if err := s.pc.SendJSON(proto.TItemDone, proto.ItemDone{SID: j.ID, ID: it.ID}); err != nil {
			m.failJob(j, err)
			return
		}
	}
	if err := s.pc.SendJSON(proto.TFinish, proto.Finish{SID: j.ID}); err != nil {
		m.failJob(j, err)
		return
	}
	s.pc.SetReadDeadline(time.Now().Add(30 * time.Second))
	var res proto.Result
	if err := s.pc.RecvJSON(proto.TResult, &res); err != nil {
		m.failJob(j, err)
		return
	}
	if !res.OK {
		m.failJob(j, errors.New(res.Err))
		return
	}
	m.setState(j, "done", "")
	m.addHistory(j, "done")
	m.runCleanup(j)
}

func (m *Manager) sendFile(j *Job, pc *proto.Conn, it *Item, buf []byte) error {
	f, err := os.Open(it.SrcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != it.Size {
		return fmt.Errorf("源文件已变化: %s", it.Name)
	}
	off := it.Offset
	if off > 0 {
		if _, err := f.Seek(off, 0); err != nil {
			return err
		}
	}
	for off < it.Size {
		if j.ctx.Err() != nil {
			return errCanceled
		}
		n := int64(len(buf))
		if rem := it.Size - off; rem < n {
			n = rem
		}
		if _, err := io.ReadFull(f, buf[:n]); err != nil {
			return err
		}
		sum := sha256.Sum256(buf[:n])
		if err := pc.SendData(it.ID, off, sum, buf[:n]); err != nil {
			return err
		}
		off += n
		m.tick(j, it, off, n)
	}
	return nil
}

func (m *Manager) SendText(peerID, addr, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := m.dialPeer(ctx, peerID, addr)
	if err != nil {
		return err
	}
	defer s.pc.Close()
	if err := s.pc.SendJSON(proto.TText, proto.Text{Body: body}); err != nil {
		return err
	}
	s.pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	var res proto.Result
	if err := s.pc.RecvJSON(proto.TResult, &res); err != nil {
		return err
	}
	if !res.OK {
		return errors.New(res.Err)
	}
	m.mu.Lock()
	m.msgs = append(m.msgs, Message{FromID: m.self.ID, FromName: m.st.Config().Name, Body: body, Time: time.Now().Unix(), Dir: "out"})
	m.mu.Unlock()
	return nil
}

func (m *Manager) recvText(pc *proto.Conn, peerID, peerName string, n int) {
	b := make([]byte, n)
	if err := pc.ReadBody(b); err != nil {
		return
	}
	var t proto.Text
	if json.Unmarshal(b, &t) != nil {
		return
	}
	m.mu.Lock()
	m.msgs = append(m.msgs, Message{FromID: peerID, FromName: peerName, Body: t.Body, Time: time.Now().Unix(), Dir: "in"})
	if len(m.msgs) > 200 {
		m.msgs = m.msgs[len(m.msgs)-200:]
	}
	m.mu.Unlock()
	pc.SendJSON(proto.TResult, proto.Result{OK: true})
}

func (m *Manager) Decide(id string, accept, always bool) error {
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil || j.Dir != "recv" || j.decide == nil {
		m.mu.Unlock()
		return errors.New("没有等待确认的接收任务")
	}
	ch := j.decide
	peerID, peerName := j.PeerID, j.PeerName
	m.mu.Unlock()
	select {
	case ch <- accept:
	default:
	}
	if accept && always {
		m.st.Trust(peerID, peerName)
	}
	return nil
}

func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil {
		m.mu.Unlock()
		return
	}
	if j.State == "done" || j.State == "failed" || j.State == "canceled" || j.State == "rejected" {
		m.mu.Unlock()
		return
	}
	j.State = "canceled"
	j.Updated = time.Now().Unix()
	cancelFn, pc, decide := j.cancelFn, j.pc, j.decide
	m.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}
	if pc != nil {
		pc.Close()
	}
	if decide != nil {
		select {
		case decide <- false:
		default:
		}
	}
	m.runCleanup(j)
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	jobs := make([]*Job, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		j := m.jobs[m.order[i]]
		if j == nil {
			continue
		}
		j.updateSpeed(now)
		jobs = append(jobs, j.clone())
	}
	msgs := append([]Message(nil), m.msgs...)
	relay := "off"
	if m.wan != nil {
		relay = m.wan.Status()
	}
	return Snapshot{Peers: m.reg.List(), Jobs: jobs, Messages: msgs, Relay: relay}
}

func (j *Job) updateSpeed(now time.Time) {
	if j.State == "active" && now.Sub(j.lastAt) >= 500*time.Millisecond {
		inst := (j.Done - j.lastDone) * int64(time.Second) / int64(now.Sub(j.lastAt))
		if inst < 0 {
			inst = 0
		}
		if j.Speed == 0 {
			j.Speed = inst
		} else {
			j.Speed = (j.Speed*2 + inst) / 3
		}
		j.lastDone = j.Done
		j.lastAt = now
		return
	}
	if j.State != "active" {
		j.Speed = 0
		j.lastDone = j.Done
		j.lastAt = now
	}
}

func (m *Manager) FindJob(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[id]; j != nil {
		j.updateSpeed(time.Now())
		return j.clone()
	}
	return nil
}

func (m *Manager) tick(j *Job, it *Item, done, add int64) {
	m.mu.Lock()
	it.Done = done
	it.Offset = done
	j.Done += add
	j.Updated = time.Now().Unix()
	m.mu.Unlock()
}

func (m *Manager) setState(j *Job, state, errMsg string) {
	m.mu.Lock()
	if j.State == "canceled" {
		m.mu.Unlock()
		return
	}
	j.State = state
	if errMsg != "" {
		j.Error = errMsg
	}
	j.Updated = time.Now().Unix()
	if state == "done" {
		j.Speed = 0
	}
	m.mu.Unlock()
	if state == "done" {
		m.runCleanup(j)
	}
}

func (m *Manager) failJob(j *Job, err error) {
	if err == nil {
		err = errors.New("未知错误")
	}
	if errors.Is(err, errCanceled) {
		return
	}
	m.mu.Lock()
	if j.State == "canceled" || j.State == "done" {
		m.mu.Unlock()
		return
	}
	j.State = "failed"
	j.Error = err.Error()
	j.Speed = 0
	j.Updated = time.Now().Unix()
	m.mu.Unlock()
	log.Printf("[传输] %s 失败: %v", j.ID, err)
	m.addHistory(j, "failed")
	m.runCleanup(j)
}

func (m *Manager) runCleanup(j *Job) {
	fn := j.cleanup
	if fn == nil {
		return
	}
	fn()
	m.mu.Lock()
	j.cleanup = nil
	m.mu.Unlock()
}

func (m *Manager) addHistory(j *Job, state string) {
	if j.Total == 0 {
		return
	}
	name := ""
	if len(j.Items) == 1 {
		name = j.Items[0].Name
	} else if len(j.Items) > 1 {
		name = fmt.Sprintf("%s 等 %d 个文件", j.Items[0].Name, len(j.Items))
	}
	m.st.AddHistory(store.History{
		Time: time.Now().Unix(), Dir: j.Dir, PeerID: j.PeerID, Peer: j.PeerName,
		Name: name, Size: j.Total, State: state,
	})
}

func (m *Manager) addJob(j *Job) {
	m.mu.Lock()
	if old := m.jobs[j.ID]; old != nil {
		for i, id := range m.order {
			if id == j.ID {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()
}

func goos() string { return runtime.GOOS }

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
