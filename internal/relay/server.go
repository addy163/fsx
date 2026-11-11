package relay

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"fsx/internal/identity"
	"fsx/internal/proto"
)

type sclient struct {
	id   string
	name string
	addr string
	port int
	conn *proto.Conn
}

type sstream struct {
	a *sclient
	b *sclient
}

// Server 同时承担信令与转发：两端都出站连它，因此不需要任何入站端口。
// 转发的是 TLS 密文，服务端看不到文件内容。
type Server struct {
	id *identity.Identity

	mu      sync.Mutex
	clients map[string]*sclient
	streams map[string]*sstream
}

func NewServer(id *identity.Identity) *Server {
	return &Server{id: id, clients: map[string]*sclient{}, streams: map[string]*sstream{}}
}

func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) handle(raw net.Conn) {
	defer raw.Close()
	tc := tls.Server(raw, s.id.ServerConfig())
	tc.SetDeadline(time.Now().Add(15 * time.Second))
	if err := tc.Handshake(); err != nil {
		return
	}
	peerID, err := identity.PeerID(tc.ConnectionState())
	if err != nil {
		return
	}
	pc := proto.New(tc)
	t, n, err := pc.Next()
	if err != nil || t != RRegister {
		return
	}
	b := make([]byte, n)
	if err := pc.ReadBody(b); err != nil {
		return
	}
	var reg Register
	if json.Unmarshal(b, &reg) != nil || reg.ID != peerID {
		return
	}
	addr := raw.RemoteAddr().String()
	host, _, _ := net.SplitHostPort(addr)
	direct := ""
	if reg.P2PPort > 0 {
		direct = net.JoinHostPort(host, strconv.Itoa(reg.P2PPort))
	}
	cl := &sclient{id: peerID, name: reg.Name, addr: addr, port: reg.P2PPort, conn: pc}

	s.mu.Lock()
	if old := s.clients[peerID]; old != nil {
		old.conn.Close()
	}
	s.clients[peerID] = cl
	s.mu.Unlock()
	log.Printf("[中继] %s 上线 (%s)", identity.Short(peerID), addr)

	pc.SendJSON(RRegistered, Registered{Observed: addr, Direct: direct})
	tc.SetDeadline(time.Time{})
	defer s.remove(cl)

	for {
		t, n, err := pc.Next()
		if err != nil {
			return
		}
		switch t {
		case RLookup:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
			var lk Lookup
			json.Unmarshal(b, &lk)
			s.mu.Lock()
			target := s.clients[lk.ID]
			if target == nil && lk.ID != "" {
				// 允许用短 ID 前缀查询，只有唯一匹配时才返回。
				q := strings.ToLower(lk.ID)
				var hit *sclient
				n := 0
				for id, c := range s.clients {
					if strings.HasPrefix(strings.ToLower(id), q) {
						hit = c
						n++
					}
				}
				if n == 1 {
					target = hit
				}
			}
			var f Found
			if target != nil {
				thost, _, _ := net.SplitHostPort(target.addr)
				f = Found{ID: target.id, Name: target.name, Q: lk.ID}
				if target.port > 0 {
					f.Direct = net.JoinHostPort(thost, strconv.Itoa(target.port))
				}
			}
			s.mu.Unlock()
			if f.ID != "" {
				pc.SendJSON(RFound, f)
			} else {
				pc.SendJSON(RNotFound, Found{ID: lk.ID, Q: lk.ID})
			}

		case ROpen:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
			var op Open
			json.Unmarshal(b, &op)
			s.mu.Lock()
			target := s.clients[op.Target]
			if target == nil {
				s.mu.Unlock()
				pc.SendJSON(ROpenFailed, Opened{SID: op.SID, Err: "对方未连接中继"})
				continue
			}
			s.streams[op.SID] = &sstream{a: cl, b: target}
			s.mu.Unlock()
			if err := target.conn.SendJSON(RIncoming, Incoming{SID: op.SID, FromID: cl.id, FromName: cl.name}); err != nil {
				s.mu.Lock()
				delete(s.streams, op.SID)
				s.mu.Unlock()
				pc.SendJSON(ROpenFailed, Opened{SID: op.SID, Err: "对方连接已断开"})
				continue
			}
			pc.SendJSON(ROpened, Opened{SID: op.SID})

		case RData:
			var sidb [8]byte
			if err := pc.ReadBody(sidb[:]); err != nil {
				return
			}
			pl := n - 8
			if pl < 0 {
				return
			}
			buf := make([]byte, pl)
			if err := pc.ReadBody(buf); err != nil {
				return
			}
			sid := hex.EncodeToString(sidb[:])
			s.mu.Lock()
			st := s.streams[sid]
			var dst *sclient
			if st != nil {
				if st.a == cl {
					dst = st.b
				} else if st.b == cl {
					dst = st.a
				}
			}
			s.mu.Unlock()
			if dst != nil {
				body := make([]byte, 8+len(buf))
				copy(body, sidb[:])
				copy(body[8:], buf)
				dst.conn.SendRaw(RData, body)
			}

		case RClose:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
			var clc Close
			json.Unmarshal(b, &clc)
			s.mu.Lock()
			st := s.streams[clc.SID]
			delete(s.streams, clc.SID)
			var dst *sclient
			if st != nil {
				if st.a == cl {
					dst = st.b
				} else if st.b == cl {
					dst = st.a
				}
			}
			s.mu.Unlock()
			if dst != nil {
				dst.conn.SendJSON(RClose, Close{SID: clc.SID})
			}

		case RPing:
			if n > 0 {
				b := make([]byte, n)
				if err := pc.ReadBody(b); err != nil {
					return
				}
			}
			pc.SendRaw(RPong, nil)

		default:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				return
			}
		}
	}
}

func (s *Server) remove(cl *sclient) {
	s.mu.Lock()
	wasCurrent := s.clients[cl.id] == cl
	if wasCurrent {
		delete(s.clients, cl.id)
	}
	var notify []*sclient
	var sids []string
	for sid, st := range s.streams {
		if st.a == cl || st.b == cl {
			if st.a == cl {
				notify = append(notify, st.b)
			} else {
				notify = append(notify, st.a)
			}
			sids = append(sids, sid)
			delete(s.streams, sid)
		}
	}
	s.mu.Unlock()
	for i, dst := range notify {
		dst.conn.SendJSON(RClose, Close{SID: sids[i]})
	}
	if wasCurrent {
		log.Printf("[中继] %s 下线", identity.Short(cl.id))
	}
}
