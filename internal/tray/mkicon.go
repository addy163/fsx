//go:build ignore

// mkicon 生成 macOS 应用图标源图（1024×1024 PNG）：品牌渐变圆角方块 + 白色上传箭头。
// 用法: go run internal/tray/mkicon.go out.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

const N = 1024

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func insideRound(x, y, r float64) bool {
	dx, dy := x-clamp(x, r, N-r), y-clamp(y, r, N-r)
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

func main() {
	img := image.NewRGBA(image.Rect(0, 0, N, N))
	arrow := [][2]float64{{16, 7}, {24, 16}, {19, 16}, {19, 25}, {13, 25}, {13, 16}, {8, 16}}
	for py := 0; py < N; py++ {
		for px := 0; px < N; px++ {
			var sr, sg, sb, hit int
			for sy := 0; sy < 2; sy++ {
				for sx := 0; sx < 2; sx++ {
					x := float64(px) + (float64(sx)+0.5)/2
					y := float64(py) + (float64(sy)+0.5)/2
					if !insideRound(x, y, 224) {
						continue
					}
					hit++
					t := (x + y) / (2 * N)
					r := 76 + t*(46-76)
					g := 141 + t*(230-141)
					b := 255 + t*(168-255)
					if inPoly(x/32, y/32, arrow) {
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
	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}