package transfer

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fsx/internal/proto"
)

type sidecar struct {
	Size int64  `json:"size"`
	Head string `json:"head"`
	Tail string `json:"tail"`
	Off  int64  `json:"off"`
}

func (m *Manager) recvOffer(pc *proto.Conn, peerID, peerName, sas string, n int) {
	b := make([]byte, n)
	if err := pc.ReadBody(b); err != nil {
		return
	}
	var off proto.Offer
	if err := json.Unmarshal(b, &off); err != nil || off.SID == "" {
		return
	}
	now := time.Now().Unix()
	j := &Job{
		ID: off.SID, Dir: "recv", PeerID: peerID, PeerName: peerName,
		State: "pending", SAS: sas, Created: now, Updated: now,
		decide: make(chan bool, 1), pc: pc,
	}
	for _, it := range off.Items {
		j.Items = append(j.Items, &Item{
			ID: it.ID, Name: it.Name, Size: it.Size, MTime: it.MTime, Hd: it.Head, Tl: it.Tail,
		})
		j.Total += it.Size
	}
	m.addJob(j)

	if !m.autoAccept(peerID) {
		// 发送方在确认前断开（例如取消）时，读取会立即返回错误。
		gone := make(chan error, 1)
		go func() {
			pc.SetReadDeadline(time.Now().Add(2 * time.Minute))
			_, _, err := pc.Next()
			gone <- err
		}()
		select {
		case ok := <-j.decide:
			pc.SetReadDeadline(time.Now()) // 让监听协程立刻退出
			<-gone
			pc.SetReadDeadline(time.Time{})
			if !ok {
				pc.SendJSON(proto.TReject, proto.Reject{SID: j.ID, Reason: "对方拒绝接收"})
				m.setState(j, "rejected", "")
				return
			}
		case <-time.After(2 * time.Minute):
			pc.SendJSON(proto.TReject, proto.Reject{SID: j.ID, Reason: "等待确认超时"})
			m.setState(j, "rejected", "等待确认超时")
			return
		case <-gone:
			m.setState(j, "canceled", "")
			return
		}
	}

	offsets, err := m.prepareRecv(j)
	if err != nil {
		pc.SendJSON(proto.TReject, proto.Reject{SID: j.ID, Reason: err.Error()})
		m.failJob(j, err)
		return
	}
	if err := pc.SendJSON(proto.TAccept, proto.Accept{SID: j.ID, Offsets: offsets}); err != nil {
		m.failJob(j, err)
		return
	}
	m.setState(j, "active", "")
	m.runRecv(pc, j)
}

func (m *Manager) prepareRecv(j *Job) (map[uint32]int64, error) {
	root := m.st.Config().DownloadDir
	offsets := map[uint32]int64{}
	var totalDone int64
	for _, it := range j.Items {
		rel, err := safeRel(it.Name)
		if err != nil {
			return nil, fmt.Errorf("非法文件名 %q", it.Name)
		}
		final := uniquePath(root, rel)
		if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
			return nil, err
		}
		it.FinalPath = final
		it.PartPath = final + ".part"
		it.Finalized = false

		if !exists(final) {
			if sc, ok := readSidecar(it.PartPath + ".fsx"); ok &&
				sc.Size == it.Size && sc.Head == it.Hd && sc.Tail == it.Tl &&
				sc.Off >= 0 && sc.Off <= it.Size {
				if fi, err := os.Stat(it.PartPath); err == nil && fi.Size() >= sc.Off {
					it.Offset = sc.Off
					it.Done = sc.Off
					offsets[it.ID] = sc.Off
					totalDone += sc.Off
					continue
				}
			}
		}
		os.Remove(it.PartPath)
		os.Remove(it.PartPath + ".fsx")
		f, err := os.OpenFile(it.PartPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, err
		}
		f.Close()
		it.Offset = 0
		it.Done = 0
		offsets[it.ID] = 0
	}
	m.mu.Lock()
	j.Done = totalDone
	j.Updated = time.Now().Unix()
	m.mu.Unlock()
	return offsets, nil
}

func (m *Manager) runRecv(pc *proto.Conn, j *Job) {
	buf := make([]byte, proto.ChunkSize)
	open := map[uint32]*os.File{}
	itemByID := map[uint32]*Item{}
	for _, it := range j.Items {
		itemByID[it.ID] = it
	}
	closeAll := func() {
		for _, f := range open {
			f.Close()
		}
	}
	defer closeAll()
	lastSave := time.Now()

	for {
		t, n, err := pc.Next()
		if err != nil {
			m.saveAllSidecars(j)
			m.failJob(j, err)
			return
		}
		switch t {
		case proto.TData:
			h, pl, err := pc.ReadDataMeta(n)
			if err != nil {
				m.saveAllSidecars(j)
				m.failJob(j, err)
				return
			}
			if pl > len(buf) || pl < 0 {
				m.failJob(j, errors.New("数据块长度非法"))
				return
			}
			if err := pc.ReadBody(buf[:pl]); err != nil {
				m.saveAllSidecars(j)
				m.failJob(j, err)
				return
			}
			it := itemByID[h.Item]
			if it == nil {
				m.failJob(j, fmt.Errorf("未知文件块 %d", h.Item))
				return
			}
			if sum := sha256.Sum256(buf[:pl]); sum != h.Sum {
				m.failJob(j, fmt.Errorf("%s 数据校验失败", it.Name))
				return
			}
			f := open[h.Item]
			if f == nil {
				f, err = os.OpenFile(it.PartPath, os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					m.failJob(j, err)
					return
				}
				open[h.Item] = f
			}
			if _, err := f.WriteAt(buf[:pl], h.Off); err != nil {
				m.failJob(j, err)
				return
			}
			m.tick(j, it, h.Off+int64(pl), int64(pl))
			if time.Since(lastSave) > 2*time.Second {
				m.saveAllSidecars(j)
				lastSave = time.Now()
			}

		case proto.TItemDone:
			b := make([]byte, n)
			if err := pc.ReadBody(b); err != nil {
				m.failJob(j, err)
				return
			}
			var d proto.ItemDone
			if json.Unmarshal(b, &d) != nil {
				continue
			}
			it := itemByID[d.ID]
			if it == nil {
				continue
			}
			if f := open[d.ID]; f != nil {
				f.Sync()
				f.Close()
				delete(open, d.ID)
			}
			if err := finalizeItem(it); err != nil {
				m.failJob(j, err)
				return
			}
			m.mu.Lock()
			it.Done = it.Size
			it.Finalized = true
			m.mu.Unlock()

		case proto.TFinish:
			m.saveAllSidecars(j)
			pc.SendJSON(proto.TResult, proto.Result{SID: j.ID, OK: true})
			m.setState(j, "done", "")
			m.addHistory(j, "done")
			return

		case proto.TCancel:
			b := make([]byte, n)
			pc.ReadBody(b)
			var c proto.Cancel
			json.Unmarshal(b, &c)
			m.saveAllSidecars(j)
			m.setState(j, "canceled", c.Reason)
			return

		default:
			m.failJob(j, fmt.Errorf("意外的帧类型 %d", t))
			return
		}
	}
}

func (m *Manager) saveAllSidecars(j *Job) {
	m.mu.Lock()
	items := make([]*Item, len(j.Items))
	copy(items, j.Items)
	m.mu.Unlock()
	for _, it := range items {
		if it.Finalized || it.PartPath == "" || it.Offset <= 0 {
			continue
		}
		sc := sidecar{Size: it.Size, Head: it.Hd, Tail: it.Tl, Off: it.Offset}
		b, err := json.Marshal(sc)
		if err != nil {
			continue
		}
		tmp := it.PartPath + ".fsx.tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			os.Rename(tmp, it.PartPath+".fsx")
		}
	}
}

func readSidecar(path string) (sidecar, bool) {
	var sc sidecar
	b, err := os.ReadFile(path)
	if err != nil {
		return sc, false
	}
	if json.Unmarshal(b, &sc) != nil {
		return sc, false
	}
	return sc, true
}

func finalizeItem(it *Item) error {
	fi, err := os.Stat(it.PartPath)
	if err != nil {
		return err
	}
	if fi.Size() != it.Size {
		return fmt.Errorf("%s 写入不完整: %d/%d", it.Name, fi.Size(), it.Size)
	}
	if err := os.Rename(it.PartPath, it.FinalPath); err != nil {
		return err
	}
	os.Remove(it.PartPath + ".fsx")
	return nil
}

func safeRel(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	var out []string
	for _, p := range strings.Split(name, "/") {
		switch p {
		case "", ".":
			continue
		case "..":
			return "", errors.New("包含上级目录")
		}
		if strings.ContainsAny(p, ":\x00") {
			return "", errors.New("包含非法字符")
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return "", errors.New("空路径")
	}
	return filepath.Join(out...), nil
}

func uniquePath(root, rel string) string {
	p := filepath.Join(root, rel)
	if !exists(p) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; i < 1000; i++ {
		c := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !exists(c) {
			return c
		}
	}
	return p
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
