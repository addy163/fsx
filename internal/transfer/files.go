package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

func buildItems(paths []string) ([]*Item, int64, error) {
	var items []*Item
	var total int64
	var id uint32 = 1
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, 0, err
		}
		if fi.IsDir() {
			base := filepath.Base(filepath.Clean(p))
			err := filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return nil
				}
				rel, err := filepath.Rel(p, path)
				if err != nil {
					return err
				}
				name := filepath.ToSlash(filepath.Join(base, rel))
				it := newItem(id, name, path, info)
				id++
				items = append(items, it)
				total += it.Size
				return nil
			})
			if err != nil {
				return nil, 0, err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		it := newItem(id, filepath.Base(p), p, fi)
		id++
		items = append(items, it)
		total += it.Size
	}
	return items, total, nil
}

func newItem(id uint32, name, src string, fi os.FileInfo) *Item {
	head, tail := fingerprint(src, fi.Size())
	return &Item{
		ID: id, Name: name, Size: fi.Size(), MTime: fi.ModTime().Unix(),
		SrcPath: src, Hd: head, Tl: tail,
	}
}

// fingerprint 用「大小 + 首尾 64KB 哈希」识别源文件，足够判断能否续传，
// 又不用为几十 GB 的文件先整体扫一遍。
func fingerprint(path string, size int64) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, buf)
	h := sha256.Sum256(buf[:n])
	head := hex.EncodeToString(h[:8])
	tail := ""
	if size > 64<<10 {
		if _, err := f.Seek(-64<<10, io.SeekEnd); err == nil {
			m, _ := io.ReadFull(f, buf)
			t := sha256.Sum256(buf[:m])
			tail = hex.EncodeToString(t[:8])
		}
	}
	return head, tail
}
