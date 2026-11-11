package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Config struct {
	Name        string   `json:"name"`
	DownloadDir string   `json:"download_dir"`
	Relay       string   `json:"relay"`
	RelayFP     string   `json:"relay_fp"`
	AutoAccept  []string `json:"auto_accept"`
	HTTPPort    int      `json:"http_port"`
	P2PPort     int      `json:"p2p_port"`
	DiscPort    int      `json:"disc_port"`
}

type History struct {
	Time   int64  `json:"time"`
	Dir    string `json:"dir"`
	PeerID string `json:"peer_id"`
	Peer   string `json:"peer_name"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
}

type peerRec struct {
	Name     string `json:"name"`
	LastSeen int64  `json:"last_seen"`
}

type stateFile struct {
	Config Config             `json:"config"`
	Peers  map[string]peerRec `json:"peers"`
}

type Store struct {
	mu    sync.Mutex
	dir   string
	cfg   Config
	peers map[string]peerRec
	hist  []History
}

func Open(dir string, defaults Config) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, peers: map[string]peerRec{}}
	path := filepath.Join(dir, "state.json")
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		var sf stateFile
		if err := json.Unmarshal(b, &sf); err != nil {
			return nil, err
		}
		s.cfg = sf.Config
		if sf.Peers != nil {
			s.peers = sf.Peers
		}
	case !os.IsNotExist(err):
		return nil, err
	}

	if s.cfg.Name == "" {
		s.cfg.Name = defaults.Name
	}
	if s.cfg.DownloadDir == "" {
		s.cfg.DownloadDir = defaults.DownloadDir
	}
	if s.cfg.HTTPPort == 0 {
		s.cfg.HTTPPort = defaults.HTTPPort
	}
	if s.cfg.P2PPort == 0 {
		s.cfg.P2PPort = defaults.P2PPort
	}
	if s.cfg.DiscPort == 0 {
		s.cfg.DiscPort = defaults.DiscPort
	}
	if err := os.MkdirAll(s.cfg.DownloadDir, 0o755); err != nil {
		return nil, err
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	s.loadHistory()
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.AutoAccept = append([]string(nil), s.cfg.AutoAccept...)
	return c
}

func (s *Store) Update(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	return s.saveLocked()
}

func (s *Store) AutoAccept(peerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.cfg.AutoAccept {
		if v == "*" || v == peerID {
			return true
		}
	}
	return false
}

func (s *Store) Trust(peerID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, v := range s.cfg.AutoAccept {
		if v == peerID {
			found = true
			break
		}
	}
	if !found {
		s.cfg.AutoAccept = append(s.cfg.AutoAccept, peerID)
	}
	if peerID != "" {
		s.peers[peerID] = peerRec{Name: name, LastSeen: time.Now().Unix()}
	}
	return s.saveLocked()
}

func (s *Store) SetRelayFP(fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.RelayFP = fp
	return s.saveLocked()
}

func (s *Store) AddHistory(h History) {
	s.mu.Lock()
	s.hist = append(s.hist, h)
	if len(s.hist) > 500 {
		s.hist = s.hist[len(s.hist)-500:]
	}
	s.mu.Unlock()
	if b, err := json.Marshal(h); err == nil {
		f, err := os.OpenFile(filepath.Join(s.dir, "history.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

func (s *Store) History() []History {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]History(nil), s.hist...)
}

func (s *Store) loadHistory() {
	b, err := os.ReadFile(filepath.Join(s.dir, "history.jsonl"))
	if err != nil {
		return
	}
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			var h History
			if json.Unmarshal(b[start:i], &h) == nil {
				s.hist = append(s.hist, h)
			}
			start = i + 1
		}
	}
	if len(s.hist) > 500 {
		s.hist = s.hist[len(s.hist)-500:]
	}
}

func (s *Store) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	sf := stateFile{Config: s.cfg, Peers: s.peers}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "state.json"))
}
