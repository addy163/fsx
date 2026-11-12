package proto

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const Version = 1

// 帧类型。控制帧体是 JSON，数据帧体是定长头 + 文件块。
const (
	THello    byte = 1
	TOffer    byte = 2
	TAccept   byte = 3
	TReject   byte = 4
	TData     byte = 5
	TItemDone byte = 6
	TFinish   byte = 7
	TResult   byte = 8
	TCancel   byte = 9
	TText     byte = 10
)

const (
	MaxBody   = 8 << 20
	ChunkSize = 4 << 20
	dataHdr   = 44 // item(4) + offset(8) + sha256(32)
)

type Hello struct {
	V    int      `json:"v"`
	ID   string   `json:"id"`
	Name string   `json:"name"`
	OS   string   `json:"os"`
	Port int      `json:"port"`
	V6   []string `json:"v6,omitempty"`
}

type Item struct {
	ID    uint32 `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Head  string `json:"head"`
	Tail  string `json:"tail"`
}

type Offer struct {
	SID   string `json:"sid"`
	Items []Item `json:"items"`
	Text  string `json:"text,omitempty"`
}

type Accept struct {
	SID     string           `json:"sid"`
	Offsets map[uint32]int64 `json:"offsets"`
}

type Reject struct {
	SID    string `json:"sid"`
	Reason string `json:"reason"`
}

type ItemDone struct {
	SID string `json:"sid"`
	ID  uint32 `json:"id"`
}

type Finish struct {
	SID string `json:"sid"`
}

type Result struct {
	SID string `json:"sid"`
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

type Cancel struct {
	SID    string `json:"sid"`
	Reason string `json:"reason"`
}

type Text struct {
	Body string `json:"body"`
}

type DataHdr struct {
	Item uint32
	Off  int64
	Sum  [32]byte
}

// Conn 是带长度前缀的帧连接，写操作串行化，读操作由单 goroutine 驱动。
type Conn struct {
	c  net.Conn
	r  *bufio.Reader
	w  *bufio.Writer
	wm sync.Mutex
}

func New(c net.Conn) *Conn {
	return &Conn{
		c: c,
		r: bufio.NewReaderSize(c, 64<<10),
		w: bufio.NewWriterSize(c, 256<<10),
	}
}

func (c *Conn) Close() error                      { return c.c.Close() }
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }
func (c *Conn) SetDeadline(t time.Time) error     { return c.c.SetDeadline(t) }
func (c *Conn) RemoteAddr() net.Addr              { return c.c.RemoteAddr() }
func (c *Conn) LocalAddr() net.Addr               { return c.c.LocalAddr() }
func (c *Conn) Raw() net.Conn                     { return c.c }

func (c *Conn) SendJSON(t byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.SendRaw(t, b)
}

func (c *Conn) SendRaw(t byte, b []byte) error {
	if len(b)+1 > MaxBody {
		return errors.New("消息过大")
	}
	c.wm.Lock()
	defer c.wm.Unlock()
	var h [5]byte
	binary.BigEndian.PutUint32(h[:4], uint32(len(b)+1))
	h[4] = t
	if _, err := c.w.Write(h[:]); err != nil {
		return err
	}
	if _, err := c.w.Write(b); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *Conn) SendData(item uint32, off int64, sum [32]byte, p []byte) error {
	n := 1 + dataHdr + len(p)
	if n > MaxBody {
		return errors.New("数据块过大")
	}
	c.wm.Lock()
	defer c.wm.Unlock()
	var h [4 + 1 + dataHdr]byte
	binary.BigEndian.PutUint32(h[:4], uint32(n))
	h[4] = TData
	binary.BigEndian.PutUint32(h[5:9], item)
	binary.BigEndian.PutUint64(h[9:17], uint64(off))
	copy(h[17:49], sum[:])
	if _, err := c.w.Write(h[:]); err != nil {
		return err
	}
	if _, err := c.w.Write(p); err != nil {
		return err
	}
	return c.w.Flush()
}

// Next 读取帧头和类型，返回帧体剩余长度，由调用方决定怎么消费。
func (c *Conn) Next() (t byte, bodyLen int, err error) {
	var h [5]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return 0, 0, err
	}
	n := int(binary.BigEndian.Uint32(h[:4]))
	if n < 1 || n > MaxBody {
		return 0, 0, fmt.Errorf("帧长度非法: %d", n)
	}
	return h[4], n - 1, nil
}

func (c *Conn) ReadBody(p []byte) error {
	_, err := io.ReadFull(c.r, p)
	return err
}

func (c *Conn) RecvJSON(want byte, v any) error {
	t, n, err := c.Next()
	if err != nil {
		return err
	}
	if t != want {
		return fmt.Errorf("期望帧 %d，收到 %d", want, t)
	}
	b := make([]byte, n)
	if err := c.ReadBody(b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ReadDataMeta 在 Next 之后调用，读出 44 字节数据头，返回负载长度。
func (c *Conn) ReadDataMeta(bodyLen int) (DataHdr, int, error) {
	var d DataHdr
	if bodyLen < dataHdr {
		return d, 0, errors.New("数据帧过短")
	}
	var h [dataHdr]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return d, 0, err
	}
	d.Item = binary.BigEndian.Uint32(h[0:4])
	d.Off = int64(binary.BigEndian.Uint64(h[4:12]))
	copy(d.Sum[:], h[12:44])
	return d, bodyLen - dataHdr, nil
}
