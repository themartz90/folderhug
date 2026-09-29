package thumbnails

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
)

const maxInput = 16 << 20
const maxPixels = 16_000_000
const maxOutput = 1 << 20

func dimensionsAllowed(config image.Config, format string, data []byte) bool {
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return false
	}
	// Go decodes 16-bit RGB/alpha PNG into eight-byte pixels.
	if format == "png" && len(data) >= 26 && data[24] == 16 && data[25] != 0 {
		return config.Width <= (64<<20)/8/config.Height
	}
	return true
}

func raster(data []byte) ([]byte, error) {
	if len(data) > maxInput {
		return nil, ErrImage
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !dimensionsAllowed(config, format, data) {
		return nil, ErrImage
	}
	if format != "jpeg" && format != "png" && format != "gif" {
		return nil, ErrImage
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrImage
	}
	orientation := 1
	if format == "jpeg" {
		orientation = exifOrientation(data)
	}
	width, height := config.Width, config.Height
	if orientation >= 5 {
		width, height = height, width
	}
	outWidth, outHeight := width, height
	if outWidth > 320 {
		outHeight = max(1, outHeight*320/outWidth)
		outWidth = 320
	}
	if outHeight > 320 {
		outWidth = max(1, outWidth*320/outHeight)
		outHeight = 320
	}
	out := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))
	bounds := decoded.Bounds()
	for y := 0; y < outHeight; y++ {
		for x := 0; x < outWidth; x++ {
			u, v := x*width/outWidth, y*height/outHeight
			sx, sy := sourcePoint(orientation, u, v, config.Width, config.Height)
			r, g, b, a := decoded.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
			out.SetRGBA(x, y, color.RGBA{uint8((r + 65535 - a) >> 8), uint8((g + 65535 - a) >> 8), uint8((b + 65535 - a) >> 8), 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, out, &jpeg.Options{Quality: 82}); err != nil || output.Len() > maxOutput {
		return nil, ErrImage
	}
	return output.Bytes(), nil
}

func sourcePoint(orientation, x, y, width, height int) (int, int) {
	switch orientation {
	case 2:
		return width - 1 - x, y
	case 3:
		return width - 1 - x, height - 1 - y
	case 4:
		return x, height - 1 - y
	case 5:
		return y, x
	case 6:
		return y, height - 1 - x
	case 7:
		return width - 1 - y, height - 1 - x
	case 8:
		return width - 1 - y, x
	}
	return x, y
}

func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			return 1
		}
		marker := data[offset+1]
		if marker == 0xda || marker == 0xd9 {
			return 1
		}
		if marker == 0xff {
			offset++
			continue
		}
		length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if length < 2 || offset+2+length > len(data) {
			return 1
		}
		segment := data[offset+4 : offset+2+length]
		if marker == 0xe1 && len(segment) >= 14 && bytes.Equal(segment[:6], []byte("Exif\x00\x00")) {
			tiff := segment[6:]
			var order binary.ByteOrder
			switch string(tiff[:2]) {
			case "II":
				order = binary.LittleEndian
			case "MM":
				order = binary.BigEndian
			default:
				return 1
			}
			if order.Uint16(tiff[2:4]) != 42 {
				return 1
			}
			start := uint64(order.Uint32(tiff[4:8]))
			if start+2 > uint64(len(tiff)) {
				return 1
			}
			count := uint64(order.Uint16(tiff[start : start+2]))
			if count > 256 || start+2+12*count > uint64(len(tiff)) {
				return 1
			}
			for i := uint64(0); i < count; i++ {
				record := tiff[start+2+12*i : start+14+12*i]
				if order.Uint16(record[:2]) == 0x0112 && order.Uint16(record[2:4]) == 3 && order.Uint32(record[4:8]) == 1 {
					value := int(order.Uint16(record[8:10]))
					if value >= 1 && value <= 8 {
						return value
					}
					return 1
				}
			}
		}
		offset += 2 + length
	}
	return 1
}

func readImage(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxInput+1))
	if err != nil || len(body) > maxInput {
		return nil, ErrImage
	}
	return body, nil
}
