package thumbnails

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func syntheticJPEG(t *testing.T, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 40))
	colors := []color.RGBA{{240, 0, 0, 255}, {0, 240, 0, 255}, {0, 0, 240, 255}, {240, 240, 0, 255}}
	for y := 0; y < 40; y++ {
		for x := 0; x < 80; x++ {
			index := 0
			if x >= 40 {
				index++
			}
			if y >= 20 {
				index += 2
			}
			img.SetRGBA(x, y, colors[index])
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0, 0, 0, 0, 0}
	segment := []byte{0xff, 0xe1, 0, byte(len(exif) + 2)}
	segment = append(segment, exif...)
	data := encoded.Bytes()
	return append(append(append([]byte{}, data[:2]...), segment...), data[2:]...)
}

func TestAllEXIFOrientationsWithoutLargeRotatedAllocation(t *testing.T) {
	// Expected quadrant order is top-left, top-right, bottom-left, bottom-right.
	orders := [][]int{{0, 1, 2, 3}, {1, 0, 3, 2}, {3, 2, 1, 0}, {2, 3, 0, 1}, {0, 2, 1, 3}, {2, 0, 3, 1}, {3, 1, 2, 0}, {1, 3, 0, 2}}
	colors := []color.RGBA{{240, 0, 0, 255}, {0, 240, 0, 255}, {0, 0, 240, 255}, {240, 240, 0, 255}}
	for orientation := 1; orientation <= 8; orientation++ {
		output, err := raster(syntheticJPEG(t, orientation))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(output, []byte("Exif")) {
			t.Fatal("EXIF metadata was retained")
		}
		img, err := jpeg.Decode(bytes.NewReader(output))
		if err != nil {
			t.Fatal(err)
		}
		width, height := 80, 40
		if orientation >= 5 {
			width, height = 40, 80
		}
		if img.Bounds().Dx() != width || img.Bounds().Dy() != height {
			t.Fatal("orientation dimensions incorrect", orientation, img.Bounds())
		}
		points := []image.Point{{width / 4, height / 4}, {3 * width / 4, height / 4}, {width / 4, 3 * height / 4}, {3 * width / 4, 3 * height / 4}}
		for index, point := range points {
			got := color.RGBAModel.Convert(img.At(point.X, point.Y)).(color.RGBA)
			want := colors[orders[orientation-1][index]]
			if difference(got.R, want.R) > 15 || difference(got.G, want.G) > 15 || difference(got.B, want.B) > 15 {
				t.Fatalf("orientation %d quadrant %d got %v want %v", orientation, index, got, want)
			}
		}
	}
}

func difference(a, b uint8) int {
	n := int(a) - int(b)
	if n < 0 {
		return -n
	}
	return n
}

func TestRasterFormatDimensionsAndCompressedLimits(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 640, 320))
	img.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 0})
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	output, err := raster(source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(output))
	if err != nil || format != "jpeg" || config.Width != 320 || config.Height != 160 {
		t.Fatal(config, format, err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r < 60000 || g < 60000 || b < 60000 {
		t.Fatal("transparent pixels were not composited on white")
	}
	oversized := append([]byte(nil), source.Bytes()...)
	binary.BigEndian.PutUint32(oversized[16:20], 100000)
	binary.BigEndian.PutUint32(oversized[20:24], 100000)
	binary.BigEndian.PutUint32(oversized[29:33], crc32.ChecksumIEEE(oversized[12:29]))
	for _, input := range [][]byte{nil, []byte("<svg>not raster</svg>"), []byte("RIFFxxxxWEBPunsupported"), oversized, bytes.Repeat([]byte{0}, maxInput+1), source.Bytes()[:35]} {
		if _, err := raster(input); err == nil {
			t.Fatal("unsafe image accepted")
		}
	}
	var animation bytes.Buffer
	if err := gif.Encode(&animation, image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.White, color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := raster(animation.Bytes()); err != nil {
		t.Fatal("GIF first frame unsupported", err)
	}
}

func TestMalformedEXIFBounds(t *testing.T) {
	for _, body := range [][]byte{{}, {0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff}, {0xff, 0xd8, 0xff, 0xe1, 0, 0}} {
		if got := exifOrientation(body); got != 1 {
			t.Fatal(got)
		}
	}
	body := syntheticJPEG(t, 1)
	binary.LittleEndian.PutUint32(body[16:20], 0xffffffff)
	if got := exifOrientation(body); got != 1 {
		t.Fatal("invalid TIFF offset accepted", got)
	}
}

func TestHighBitDepthPNGDecodedMemoryBudget(t *testing.T) {
	header := make([]byte, 26)
	header[24] = 16
	header[25] = 6
	if dimensionsAllowed(image.Config{Width: 4000, Height: 3000}, "png", header) {
		t.Fatal("16-bit RGBA exceeded64MiB decoded memory")
	}
	if !dimensionsAllowed(image.Config{Width: 2000, Height: 3000}, "png", header) {
		t.Fatal("bounded16-bit RGBA rejected")
	}
	header[25] = 0
	if !dimensionsAllowed(image.Config{Width: 4000, Height: 3000}, "png", header) {
		t.Fatal("bounded16-bit gray rejected")
	}
}
