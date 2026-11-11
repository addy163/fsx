package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const alpn = "fsx/1"

// Identity 是节点的长期身份。peer ID 由公钥哈希派生，证书每次启动现签，
// 因此对端只需要固定 ID，不需要信任任何 CA。
type Identity struct {
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
	ID   string
	Cert tls.Certificate
}

func Load(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "identity.key")
	seed, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, seed, 0o600); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("identity.key 长度异常: %d", len(seed))
	}
	return FromSeed(seed)
}

func FromSeed(seed []byte) (*Identity, error) {
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: IDOf(pub)},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Identity{
		Priv: priv,
		Pub:  pub,
		ID:   IDOf(pub),
		Cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf},
	}, nil
}

func IDOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:16])
}

func (i *Identity) ServerConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{i.Cert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{alpn},
	}
}

func (i *Identity) ClientConfig() *tls.Config {
	// 身份靠证书公钥 == 对方声明的 peer ID 来固定，不走 CA 校验。
	return &tls.Config{
		Certificates:       []tls.Certificate{i.Cert},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{alpn},
	}
}

// PeerID 从 TLS 连接状态里取出对端证书公钥并派生出 ID。
func PeerID(cs tls.ConnectionState) (string, error) {
	if len(cs.PeerCertificates) == 0 {
		return "", errors.New("对端没有提供证书")
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("对端公钥不是 Ed25519")
	}
	return IDOf(pub), nil
}

// SAS 由 TLS 导出密钥与双方 ID 派生，首连时两侧显示同一串 6 位数字。
func SAS(cs tls.ConnectionState, a, b string) string {
	if a > b {
		a, b = b, a
	}
	km, err := cs.ExportKeyingMaterial("fsx-sas-v1", []byte(a+"|"+b), 8)
	if err != nil {
		return ""
	}
	n := binary.BigEndian.Uint64(km) % 1000000
	return fmt.Sprintf("%06d", n)
}

func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
