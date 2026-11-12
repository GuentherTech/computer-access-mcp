// Command iconbuild generates the shared Windows and tray icon.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	sizes := []int{16, 20, 24, 32, 48, 64, 128, 256}
	header := make([]byte, 6+16*len(sizes))
	binary.LittleEndian.PutUint16(header[2:], 1)
	binary.LittleEndian.PutUint16(header[4:], uint16(len(sizes)))
	var payload bytes.Buffer
	for i, size := range sizes {
		im := renderIcon(size)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, im); err != nil {
			panic(err)
		}
		entry := header[6+i*16:]
		entry[0], entry[1] = byte(size), byte(size)
		binary.LittleEndian.PutUint16(entry[4:], 1)
		binary.LittleEndian.PutUint16(entry[6:], 32)
		binary.LittleEndian.PutUint32(entry[8:], uint32(encoded.Len()))
		binary.LittleEndian.PutUint32(entry[12:], uint32(len(header)+payload.Len()))
		payload.Write(encoded.Bytes())
	}
	output := append(header, payload.Bytes()...)
	if err := os.WriteFile("assets/app.ico", output, 0644); err != nil {
		panic(err)
	}
}

// Render at subpixel positions so the same artwork stays crisp at tray and installer sizes.
func renderIcon(size int) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, size, size))
	rounded := func(x, y, left, top, width, height, radius float64) bool {
		dx := math.Max(math.Abs(x-left-width/2)-(width/2-radius), 0)
		dy := math.Max(math.Abs(y-top-height/2)-(height/2-radius), 0)
		return dx*dx+dy*dy <= radius*radius
	}
	const samples = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var red, green, blue, alpha uint32
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/samples) * 64 / float64(size)
					py := (float64(y) + (float64(sy)+0.5)/samples) * 64 / float64(size)
					if !rounded(px, py, 2, 2, 60, 60, 14) {
						continue
					}
					t := (px + py) / 128
					c := color.NRGBA{R: uint8(52 - 23*t), G: uint8(139 - 57*t), B: uint8(238 - 42*t), A: 255}
					monitor := rounded(px, py, 13, 16, 38, 28, 3) && !rounded(px, py, 16, 19, 32, 22, 1)
					stand := rounded(px, py, 30, 43, 4, 7, 1) || rounded(px, py, 23, 49, 18, 3, 1.5)
					if monitor || stand {
						c = color.NRGBA{R: 245, G: 250, B: 255, A: 255}
					}
					red += uint32(c.R)
					green += uint32(c.G)
					blue += uint32(c.B)
					alpha += 255
				}
			}
			if alpha > 0 {
				im.SetNRGBA(x, y, color.NRGBA{R: uint8(red * 255 / alpha), G: uint8(green * 255 / alpha), B: uint8(blue * 255 / alpha), A: uint8(alpha / (samples * samples))})
			}
		}
	}
	return im
}
