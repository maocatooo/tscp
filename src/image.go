package tscp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// 剪贴板 DIB(去掉了 14 字节 BITMAPFILEHEADER 的 BMP) 转 PNG。
// 不用通用 BMP 解码器: x/image/bmp 只认头长 40/108/124 且压缩方式必须为 0,
// 而截图工具普遍给的是 "32bpp + BI_BITFIELDS + 40 字节头"(带 alpha 掩码), 会被判不支持。

var errUnsupportedDIB = errors.New("DIB 格式不支持(仅支持未压缩的 1/4/8/16/24/32 位图)")

// dibToPNG 把剪贴板 DIB 位图转成 PNG 字节
func dibToPNG(dib []byte) ([]byte, bool) {
	img, err := dibToImage(dib)
	if err != nil {
		return nil, false
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// dibToImage 解析剪贴板 DIB 为 image.Image
func dibToImage(dib []byte) (image.Image, error) {
	if len(dib) < 12 {
		return nil, errUnsupportedDIB
	}
	hdrSize := int(binary.LittleEndian.Uint32(dib[0:4]))
	if hdrSize < 12 || hdrSize > len(dib) {
		return nil, errUnsupportedDIB
	}

	var (
		width, height                  int
		bpp                            int
		compression                    uint32
		clrUsed                        uint32
		coreHeader                     bool
		rMask, gMask, bMask, alphaMask uint32
	)
	switch {
	case hdrSize == 12: // BITMAPCOREHEADER
		coreHeader = true
		width = int(int16(binary.LittleEndian.Uint16(dib[4:6])))
		height = int(int16(binary.LittleEndian.Uint16(dib[6:8])))
		bpp = int(binary.LittleEndian.Uint16(dib[10:12]))
	default: // 40/52/56/64/108/124 字节头, 字段偏移与 BITMAPINFOHEADER 一致
		if hdrSize < 16 {
			return nil, errUnsupportedDIB
		}
		width = int(int32(binary.LittleEndian.Uint32(dib[4:8])))
		height = int(int32(binary.LittleEndian.Uint32(dib[8:12])))
		bpp = int(binary.LittleEndian.Uint16(dib[14:16]))
		compression = binary.LittleEndian.Uint32(dib[16:20])
		if hdrSize >= 36 {
			clrUsed = binary.LittleEndian.Uint32(dib[32:36])
		}
	}

	topDown := height < 0
	if topDown {
		height = -height
	}
	// 截图常见尺寸远小于此; 上限用于挡住畸形头引发的巨额内存分配
	const maxSide, maxPixels = 20000, 100 << 20
	if width <= 0 || height <= 0 || width > maxSide || height > maxSide || width*height > maxPixels {
		return nil, errUnsupportedDIB
	}
	switch compression {
	case 0: // BI_RGB
	case 3: // BI_BITFIELDS: 掩码紧跟 40 字节头(V2 及以后则含在头里, 偏移同为 40)
		if len(dib) < 52 {
			return nil, errUnsupportedDIB
		}
		rMask = binary.LittleEndian.Uint32(dib[40:44])
		gMask = binary.LittleEndian.Uint32(dib[44:48])
		bMask = binary.LittleEndian.Uint32(dib[48:52])
		// 40 字节头后只跟 3 个掩码, 没有 alpha; V2(52) 及以后的头里才带 alpha 掩码
		if hdrSize >= 52 && len(dib) >= 56 {
			alphaMask = binary.LittleEndian.Uint32(dib[52:56])
		}
	default: // RLE 压缩等
		return nil, fmt.Errorf("%w: 压缩方式 %d", errUnsupportedDIB, compression)
	}

	// 调色板起点: BI_BITFIELDS 且 40 字节头时, 前面还有 12 字节掩码
	paletteAt := hdrSize
	if compression == 3 && hdrSize == 40 {
		paletteAt = 52
	}
	entrySize := 4
	if coreHeader {
		entrySize = 3
	}
	entries := int(clrUsed)
	if entries == 0 && bpp <= 8 {
		entries = 1 << bpp
	}
	palette := make([]color.RGBA, entries)
	for i := 0; i < entries; i++ {
		at := paletteAt + i*entrySize
		if at+entrySize > len(dib) {
			return nil, errUnsupportedDIB
		}
		palette[i] = color.RGBA{B: dib[at+2], G: dib[at+1], R: dib[at], A: 0xFF}
	}

	pixAt := paletteAt + entries*entrySize
	stride := (bpp*width + 31) / 32 * 4
	if pixAt+stride*height > len(dib) {
		return nil, errUnsupportedDIB // 像素数据不足, 不是完整剪贴板 DIB
	}

	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	for row := 0; row < height; row++ {
		y := row
		if !topDown {
			y = height - 1 - row
		}
		line := dib[pixAt+row*stride:]
		out := dst.Pix[y*dst.Stride : y*dst.Stride+width*4]
		switch bpp {
		case 1, 4, 8:
			for x := 0; x < width; x++ {
				idx := 0
				switch bpp {
				case 8:
					idx = int(line[x])
				case 4:
					b := line[x/2]
					if x%2 == 0 {
						idx = int(b >> 4)
					} else {
						idx = int(b & 0x0F)
					}
				case 1:
					if line[x/8]>>(7-uint(x%8))&1 == 1 {
						idx = 1
					}
				}
				if idx >= len(palette) {
					idx = 0
				}
				copyPixel(out[x*4:], palette[idx])
			}
		case 16:
			useMasks := rMask != 0 || gMask != 0 || bMask != 0
			for x := 0; x < width; x++ {
				v := uint32(line[x*2]) | uint32(line[x*2+1])<<8
				var r, g, b uint8
				if useMasks {
					r, g, b = extractChannel(v, rMask), extractChannel(v, gMask), extractChannel(v, bMask)
				} else {
					// BI_RGB 16bpp 即 555
					r, g, b = expand5to8(uint8(v>>10)), expand5to8(uint8(v>>5)), expand5to8(uint8(v))
				}
				copyPixel(out[x*4:], color.RGBA{R: r, G: g, B: b, A: 0xFF})
			}
		case 24:
			for x := 0; x < width; x++ {
				copyPixel(out[x*4:], color.RGBA{R: line[x*3+2], G: line[x*3+1], B: line[x*3], A: 0xFF})
			}
		case 32:
			useMasks := rMask != 0 || gMask != 0 || bMask != 0
			for x := 0; x < width; x++ {
				v := uint32(line[x*4]) | uint32(line[x*4+1])<<8 |
					uint32(line[x*4+2])<<16 | uint32(line[x*4+3])<<24
				var r, g, b, a uint8
				if useMasks {
					r, g, b = extractChannel(v, rMask), extractChannel(v, gMask), extractChannel(v, bMask)
				} else {
					// BI_RGB 32bpp: BGRA, alpha 无意义, 按不透明处理
					r, g, b = uint8(v>>16), uint8(v>>8), uint8(v)
				}
				a = 0xFF
				if alphaMask != 0 {
					a = extractChannel(v, alphaMask)
				}
				copyPixel(out[x*4:], color.RGBA{R: r, G: g, B: b, A: a})
			}
		default:
			return nil, errUnsupportedDIB
		}
	}
	return dst, nil
}

func copyPixel(dst []byte, c color.RGBA) {
	dst[0], dst[1], dst[2], dst[3] = c.R, c.G, c.B, c.A
}

// extractChannel 按掩码从像素值里取通道并归一化到 0~255
func extractChannel(v, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := uint(0)
	for mask&1 == 0 && shift < 32 {
		mask >>= 1
		shift++
	}
	bits := uint(0)
	for mask&1 == 1 && bits < 32 {
		mask >>= 1
		bits++
	}
	if bits == 0 || shift >= 32 {
		return 0
	}
	val := uint64(v>>shift) & ((uint64(1) << bits) - 1)
	if bits == 8 {
		return uint8(val)
	}
	return uint8(val * 255 / ((uint64(1) << bits) - 1))
}

// expand5to8 把 5 位色深展开到 8 位(位复制法)
func expand5to8(v uint8) uint8 {
	v &= 0x1F
	return uint8(v<<3 | v>>2)
}
