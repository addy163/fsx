//go:build ignore

// mkico 生成 Windows 应用图标 fsx.ico（多尺寸，PNG 帧）。
// 设计与 mac 一致：品牌渐变圆角方块 + 白色上传箭头。
// 用法: go run internal/tray/mkico.go internal/tray/win/fsx.ico
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
)

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func insideRound(x, y, r, n float64) bool {
	dx, dy := x-clamp(x, r, n-r), y-clamp(y, r, n-r)
	return dx*dx+dy*dy <= r*r
}

func inPoly(x, y float64, poly [][2]float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// 箭头多边形，坐标在 32 单位空间内（与 mac 图标一致）。
var arrow = [][2]float64{{16, 7}, {24, 16}, {19, 16}, {19, 25}, {13, 25}, {13, 16}, {8, 16}}

func render(n int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	nf := float64(n)
	radius := 0.21875 * nf // 224/1024
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			var sr, sg, sb, hit int
			for sy := 0; sy < 2; sy++ {
				for sx := 0; sx < 2; sx++ {
					x := float64(px) + (float64(sx)+0.5)/2
					y := float64(py) + (float64(sy)+0.5)/2
					if !insideRound(x, y, radius, nf) {
						continue
					}
					hit++
					t := (x + y) / (2 * nf)
					r := 76 + t*(46-76)
					g := 141 + t*(230-141)
					b := 255 + t*(168-255)
					if inPoly(x*32/nf, y*32/nf, arrow) {
						r, g, b = 255, 255, 255
					}
					sr += int(r)
					sg += int(g)
					sb += int(b)
				}
			}
			if hit == 0 {
				continue
			}
			img.SetRGBA(px, py, color.RGBA{
				uint8(sr / hit), uint8(sg / hit), uint8(sb / hit), uint8(255 * hit / 4),
			})
		}
	}
	return img
}

func main() {
	if len(os.Args) < 2 {
		panic("用法: go run internal/tray/mkico.go out.ico")
	}
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	frames := make([][]byte, len(sizes))
	for i, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(s)); err != nil {
			panic(err)
		}
		frames[i] = buf.Bytes()
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0))     // reserved
	binary.Write(&out, binary.LittleEndian, uint16(1))     // type: icon
	binary.Write(&out, binary.LittleEndian, uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := byte(s)
		if s == 256 {
			w = 0
		}
		out.Write([]byte{w, w, 0, 0})                     // w,h,colors,reserved
		binary.Write(&out, binary.LittleEndian, uint16(1)) // planes
		binary.Write(&out, binary.LittleEndian, uint16(32))
		binary.Write(&out, binary.LittleEndian, uint32(len(frames[i])))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(frames[i])
	}
	for _, f := range frames {
		out.Write(f)
	}
	if err := os.WriteFile(os.Args[1], out.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
