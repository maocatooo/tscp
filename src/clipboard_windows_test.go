//go:build windows

package tscp

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 这些测试会真实读写系统剪贴板, 用 TSCP_CLIPTEST=1 显式开启
func clipTestOn(t *testing.T) {
	t.Helper()
	if os.Getenv("TSCP_CLIPTEST") != "1" {
		t.Skip("设置 TSCP_CLIPTEST=1 才运行剪贴板集成测试")
	}
}

var (
	pGlobalAlloc    = kernel32.NewProc("GlobalAlloc")
	pEmptyClipboard = user32.NewProc("EmptyClipboard")
	pSetClipboard   = user32.NewProc("SetClipboardData")
)

// setClipboardGlobal 用一个全局内存块覆盖剪贴板的某个格式
func setClipboardGlobal(format uintptr, data []byte) {
	h, _, _ := pGlobalAlloc.Call(0x0002|0x0040, uintptr(len(data))) // GMEM_MOVEABLE|ZEROINIT
	if h == 0 {
		return
	}
	ptr, _, _ := pGlobalLock.Call(h)
	pMemcpy.Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	pGlobalUnlock.Call(h)
	pOpenClipboard.Call(0)
	pEmptyClipboard.Call()
	pSetClipboard.Call(format, h)
	pCloseClipboard.Call()
}

// testPNG 造一张 2x2 的 PNG
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(1, 1, color.RGBA{0, 128, 255, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testDIB 造一张 2x2 24bpp 的剪贴板 DIB(不带 14 字节文件头)
func testDIB(t *testing.T) []byte {
	t.Helper()
	const w, h = 2, 2
	rowSize := ((24*w + 31) / 32) * 4 // 每行按 4 字节对齐
	pixels := make([]byte, rowSize*h)
	for i := range pixels {
		pixels[i] = 0x80 // B=G=R=128
	}
	dib := make([]byte, 40)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], w)
	binary.LittleEndian.PutUint32(dib[8:12], h)
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 24)
	return append(dib, pixels...)
}

func TestClipboardImageDIB(t *testing.T) {
	clipTestOn(t)
	setClipboardGlobal(cfDIB, testDIB(t))

	data, present, err := readClipboardImage()
	if !present || err != nil {
		t.Fatalf("DIB 截图未识别: present=%v err=%v", present, err)
	}
	img, derr := png.Decode(bytes.NewReader(data))
	if derr != nil || img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("转出的 PNG 无效: %v %v", img, derr)
	}
	if _, image := peekClipboard(); !image {
		t.Fatal("peekClipboard 应识别为图片")
	}
}

func TestClipboardImagePNGFormat(t *testing.T) {
	clipTestOn(t)
	if cfPNG == 0 {
		t.Skip("PNG 注册格式不可用")
	}
	want := testPNG(t)
	setClipboardGlobal(cfPNG, want)

	data, present, err := readClipboardImage()
	if !present || err != nil || !bytes.Equal(data, want) {
		t.Fatalf("PNG 格式截图读取不一致: present=%v err=%v", present, err)
	}
}

func TestReadClipboardPathsScreenshot(t *testing.T) {
	clipTestOn(t)
	setClipboardGlobal(cfDIB, testDIB(t))

	paths, err := readClipboardPaths()
	if err != nil || len(paths) != 1 {
		t.Fatalf("截图应转为 1 个临时文件: %v %v", paths, err)
	}
	st, err := os.Stat(paths[0])
	if err != nil || st.Size() == 0 {
		t.Fatalf("临时截图文件无效: %v %v", paths[0], err)
	}
	t.Logf("截图保存为: %s (%d 字节)", paths[0], st.Size())
}

var _ = windows.Handle(0)
