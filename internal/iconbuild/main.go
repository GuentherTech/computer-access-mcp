// Command iconbuild generates the shared Windows and tray icon.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

func main() {
	sizes := []int{16, 20, 24, 32, 48, 64, 128, 256}
	header := make([]byte, 6+16*len(sizes))
	binary.LittleEndian.PutUint16(header[2:], 1)
	binary.LittleEndian.PutUint16(header[4:], uint16(len(sizes)))
	var payload bytes.Buffer
	for i, size := range sizes {
		im := image.NewNRGBA(image.Rect(0, 0, size, size))
		draw.Draw(im, im.Bounds(), image.NewUniform(color.NRGBA{R: 49, G: 91, B: 148, A: 255}), image.Point{}, draw.Src)
		scale := func(v int) int { return (v*size + 32) / 64 }
		stroke := max(1, scale(3))
		white := image.NewUniform(color.NRGBA{R: 240, G: 245, B: 255, A: 255})
		rect := func(x, y, w, h int) {
			r := image.Rect(x, y, x+w, y+h)
			draw.Draw(im, r, white, image.Point{}, draw.Src)
		}
		left, right, top, bottom := scale(10), scale(54), scale(13), scale(43)
		rect(left, top, right-left, stroke)
		rect(left, bottom-stroke, right-left, stroke)
		rect(left, top, stroke, bottom-top)
		rect(right-stroke, top, stroke, bottom-top)
		rect(scale(30), bottom, stroke, scale(8))
		rect(scale(22), scale(50), scale(20), stroke)
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
