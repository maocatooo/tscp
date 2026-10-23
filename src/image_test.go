package tscp

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"image/png"
	"testing"
)

// buildDIB 拼一个剪贴板 DIB
func buildDIB(hdr []byte, palette []byte, pixels []byte) []byte {
	return append(append(append([]byte{}, hdr...), palette...), pixels...)
}

func infoHeader(w, h int32, bpp int, compression uint32) []byte {
	hdr := make([]byte, 40)
	binary.LittleEndian.PutUint32(hdr[0:4], 40)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(w))
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(h))
	binary.LittleEndian.PutUint16(hdr[12:14], 1)
	binary.LittleEndian.PutUint16(hdr[14:16], uint16(bpp))
	binary.LittleEndian.PutUint32(hdr[16:20], compression)
	return hdr
}

// 真实截图工具的典型格式: 32bpp + BI_BITFIELDS + 40 字节头(带 alpha 掩码)
// 之前 x/image/bmp 正是拒绝这个组合, 导致回退成 bmp
func TestDib32Bitfields(t *testing.T) {
	const w, h = 2, 2
	hdr := infoHeader(w, h, 32, 3)
	masks := make([]byte, 12)
	binary.LittleEndian.PutUint32(masks[0:4], 0x00FF0000) // R
	binary.LittleEndian.PutUint32(masks[4:8], 0x0000FF00) // G
	binary.LittleEndian.PutUint32(masks[8:12], 0x000000FF)
	pix := []byte{
		// 每行 8 字节(2 像素 × 4 字节); 自底向上, 第 1 行是图像底行
		0x10, 0x20, 0x30, 0xFF, 0x40, 0x50, 0x60, 0xFF,
		0x70, 0x80, 0x90, 0xFF, 0xA0, 0xB0, 0xC0, 0xFF,
	}
	img, err := dibToImage(buildDIB(hdr, masks, pix))
	if err != nil {
		t.Fatalf("32bpp BI_BITFIELDS DIB 应可解码: %v", err)
	}
	if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Fatalf("尺寸: %v", img.Bounds())
	}
	// BMP 自底向上存储: 内存第一行是图像的底行
	if r, g, b, a := img.At(0, 1).RGBA(); r>>8 != 0x30 || g>>8 != 0x20 || b>>8 != 0x10 || a>>8 != 0xFF {
		t.Fatalf("底行像素 = %02X %02X %02X %02X", r>>8, g>>8, b>>8, a>>8)
	}
	if r, g, b, a := img.At(0, 0).RGBA(); r>>8 != 0x90 || g>>8 != 0x80 || b>>8 != 0x70 || a>>8 != 0xFF {
		t.Fatalf("顶行像素 = %02X %02X %02X %02X", r>>8, g>>8, b>>8, a>>8)
	}
	data, ok := dibToPNG(buildDIB(hdr, masks, pix))
	if !ok || len(data) < 8 || !bytes.Equal(data[1:4], []byte("PNG")) {
		t.Fatal("应输出 PNG")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
}

func TestDib24BitBottomUp(t *testing.T) {
	// 底向上存储: 第一行像素是图像的最底行
	hdr := infoHeader(1, 2, 24, 0)
	pix := []byte{
		0x11, 0x22, 0x33, 0x00, // 底行: B=11 G=22 R=33
		0x44, 0x55, 0x66, 0x00, // 顶行: B=44 G=55 R=66
	}
	img, err := dibToImage(buildDIB(hdr, nil, pix))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := img.At(0, 0).RGBA(); r>>8 != 0x66 || g>>8 != 0x55 || b>>8 != 0x44 {
		t.Fatalf("顶行像素: %02X %02X %02X", r>>8, g>>8, b>>8)
	}
	if r, _, _, _ := img.At(0, 1).RGBA(); r>>8 != 0x33 {
		t.Fatalf("底行像素: %02X", r>>8)
	}
}

func TestDib8BitPalette(t *testing.T) {
	hdr := make([]byte, 40)
	binary.LittleEndian.PutUint32(hdr[0:4], 40)
	binary.LittleEndian.PutUint32(hdr[4:8], 2)
	binary.LittleEndian.PutUint32(hdr[8:12], 1)
	binary.LittleEndian.PutUint16(hdr[12:14], 1)
	binary.LittleEndian.PutUint16(hdr[14:16], 8)
	binary.LittleEndian.PutUint32(hdr[32:36], 2) // biClrUsed
	palette := []byte{
		0x00, 0x00, 0xFF, 0x00, // 红
		0xFF, 0x00, 0x00, 0x00, // 蓝
	}
	pix := []byte{0x01, 0x00, 0x00, 0x00} // 行按 4 字节对齐
	img, err := dibToImage(buildDIB(hdr, palette, pix))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, b, _ := img.At(0, 0).RGBA(); r>>8 != 0xFF || b>>8 != 0x00 {
		t.Fatalf("调色板索引0 应为红: %02X %02X", r>>8, b>>8)
	}
	if r, _, b, _ := img.At(1, 0).RGBA(); r>>8 != 0x00 || b>>8 != 0xFF {
		t.Fatalf("调色板索引1 应为蓝: %02X %02X", r>>8, b>>8)
	}
}

func TestDib16Bit565(t *testing.T) {
	hdr := infoHeader(1, 1, 16, 3)
	masks := make([]byte, 12)
	binary.LittleEndian.PutUint32(masks[0:4], 0xF800)
	binary.LittleEndian.PutUint32(masks[4:8], 0x07E0)
	binary.LittleEndian.PutUint32(masks[8:12], 0x001F)
	img, err := dibToImage(buildDIB(hdr, masks, []byte{0x00, 0xF8, 0x00, 0x00})) // 565 纯红=0xF800, 行对齐
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := img.At(0, 0).RGBA()
	if r>>8 < 0xF0 || b>>8 > 0x20 {
		t.Fatalf("565 纯红解码异常: %02X %02X", r>>8, b>>8)
	}
}

func TestDibV5HeaderWithAlphaMask(t *testing.T) {
	// BITMAPV5HEADER(124 字节): 掩码与 alpha 掩码都在头里
	hdr := infoHeader(1, 1, 32, 3)
	v5 := make([]byte, 124)
	copy(v5, hdr)
	binary.LittleEndian.PutUint32(v5[0:4], 124)
	binary.LittleEndian.PutUint32(v5[40:44], 0x00FF0000)
	binary.LittleEndian.PutUint32(v5[44:48], 0x0000FF00)
	binary.LittleEndian.PutUint32(v5[48:52], 0x000000FF)
	binary.LittleEndian.PutUint32(v5[52:56], 0xFF000000) // alpha 在最高字节
	img, err := dibToImage(buildDIB(v5, nil, []byte{0x11, 0x22, 0x33, 0x80}))
	if err != nil {
		t.Fatalf("V5 头 DIB 应可解码: %v", err)
	}
	// 注意: image.NRGBA 经 .RGBA() 会预乘 alpha, 这里取非预乘值断言
	c := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA)
	if c.R != 0x33 || c.G != 0x22 || c.B != 0x11 {
		t.Fatalf("V5 像素: %02X %02X %02X", c.R, c.G, c.B)
	}
	if c.A != 0x80 {
		t.Fatalf("V5 alpha 应为 0x80: %02X", c.A)
	}
}

func TestDibUnsupported(t *testing.T) {
	// RLE 压缩应明确报错
	hdr := infoHeader(2, 2, 8, 1)
	if _, err := dibToImage(buildDIB(hdr, nil, make([]byte, 64))); err == nil {
		t.Fatal("RLE 压缩应报错")
	}
	// 像素数据不足
	if _, err := dibToImage(buildDIB(infoHeader(10, 10, 24, 0), nil, make([]byte, 16))); err == nil {
		t.Fatal("数据不足应报错")
	}
	// 空输入
	if _, err := dibToImage(nil); err == nil {
		t.Fatal("空输入应报错")
	}
}
