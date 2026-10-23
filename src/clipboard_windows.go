//go:build windows

package tscp

import (
	"fmt"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/atotto/clipboard"
	"golang.org/x/sys/windows"
)

// 剪贴板格式: 资源管理器复制的文件列表 / DIB 位图(截图) / Unicode 文本
const cfDIB = 8

const cfHDrop = 15

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	msvcrt           = windows.NewLazySystemDLL("msvcrt.dll")
	pOpenClipboard   = user32.NewProc("OpenClipboard")
	pCloseClipboard  = user32.NewProc("CloseClipboard")
	pIsFormatAvail   = user32.NewProc("IsClipboardFormatAvailable")
	pGetClipboardDta = user32.NewProc("GetClipboardData")
	pGlobalLock      = kernel32.NewProc("GlobalLock")
	pGlobalUnlock    = kernel32.NewProc("GlobalUnlock")
	pGlobalSize      = kernel32.NewProc("GlobalSize")
	pDragQueryFileW  = shell32.NewProc("DragQueryFileW")
	pClipboardSeqNum = user32.NewProc("GetClipboardSequenceNumber")
	pRegisterFmtW    = user32.NewProc("RegisterClipboardFormatW")
	pMemcpy          = msvcrt.NewProc("memcpy")
)

// cfPNG 是 "PNG" 注册格式: 截图工具(Snipaste 等)普遍会把
// 截图同时以 PNG 文件内容的形式放上剪贴板, 优先取它可免转码
var cfPNG = func() uintptr {
	name, err := windows.UTF16PtrFromString("PNG")
	if err != nil {
		return 0
	}
	r, _, _ := pRegisterFmtW.Call(uintptr(unsafe.Pointer(name)))
	return r
}()

// clipThread 把剪贴板操作固定在同一个 OS 线程上执行。
// Windows 的剪贴板锁与线程绑定: OpenClipboard 与 CloseClipboard 落在不同线程时,
// 锁不会被释放, 整个系统的剪贴板就此卡死。
func clipThread(fn func() bool) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return fn()
}

// openClipboard 打开剪贴板(必须在 clipThread 内调用); 刚被其他程序占用时短暂重试
func openClipboard() bool {
	for i := 0; i < 10; i++ {
		if r, _, _ := pOpenClipboard.Call(0); r != 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// clipboardSequence 返回剪贴板序号(内容每次变化 +1), 用于轮询检测"复制文件"
func clipboardSequence() uint32 {
	r, _, _ := pClipboardSeqNum.Call()
	return uint32(r)
}

// readClipboardPaths 读剪贴板: 文件列表 > 截图(存临时文件) > 文本路径
func readClipboardPaths() ([]string, error) {
	if r, _, _ := pIsFormatAvail.Call(cfHDrop); r != 0 {
		if paths, ok := readDroppedFiles(); ok && len(paths) > 0 {
			return paths, nil
		}
	}
	if data, present, err := readClipboardImage(); present {
		if err != nil {
			return nil, err
		}
		if p, serr := saveScreenshotTmp(data, "png"); serr == nil {
			return []string{p}, nil
		}
	}
	text, err := clipboard.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取剪贴板失败: %w", err)
	}
	return parseTextLines(text), nil
}

// peekClipboard 无副作用地探测剪贴板内容, 供轮询提示用(不落盘)
func peekClipboard() (paths []string, image bool) {
	if r, _, _ := pIsFormatAvail.Call(cfHDrop); r != 0 {
		if ps, ok := readDroppedFiles(); ok && len(ps) > 0 {
			return ps, false
		}
	}
	if cfPNG != 0 {
		if r, _, _ := pIsFormatAvail.Call(cfPNG); r != 0 {
			return nil, true
		}
	}
	if r, _, _ := pIsFormatAvail.Call(cfDIB); r != 0 {
		return nil, true
	}
	if text, err := clipboard.ReadAll(); err == nil {
		return parseTextLines(text), false
	}
	return nil, false
}

// parseTextLines 把多行文本解析为非空行
func parseTextLines(text string) []string {
	var paths []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// readClipboardImage 读取剪贴板截图, 一律转成 PNG
// 返回: PNG 字节; 剪贴板是否存在图片; 存在但无法转换时的错误
func readClipboardImage() (data []byte, present bool, err error) {
	var rawPNG, dib []byte
	clipThread(func() bool {
		if !openClipboard() {
			return false
		}
		defer pCloseClipboard.Call()

		if cfPNG != 0 {
			if r, _, _ := pIsFormatAvail.Call(cfPNG); r != 0 {
				rawPNG = readGlobal(cfPNG)
			}
		}
		if len(rawPNG) == 0 {
			if r, _, _ := pIsFormatAvail.Call(cfDIB); r != 0 {
				dib = readGlobal(cfDIB)
			}
		}
		return true
	})

	// 解码放在锁外: 大图转 PNG 可能耗时较久, 不能占着剪贴板
	switch {
	case len(rawPNG) > 0:
		return rawPNG, true, nil
	case len(dib) > 0:
		pngData, ok := dibToPNG(dib)
		if !ok {
			return nil, true, fmt.Errorf("剪贴板图片无法转换: %w", errUnsupportedDIB)
		}
		return pngData, true, nil
	}
	return nil, false, nil
}

// readGlobal 读取一个剪贴板全局内存块(用 memcpy 拷出, 避免指针转换); 必须在 clipThread 内调用
func readGlobal(format uintptr) []byte {
	h, _, _ := pGetClipboardDta.Call(format)
	if h == 0 {
		return nil
	}
	ptr, _, _ := pGlobalLock.Call(h)
	if ptr == 0 {
		return nil
	}
	defer pGlobalUnlock.Call(h)
	sz, _, _ := pGlobalSize.Call(h)
	if sz == 0 || sz > 256<<20 {
		return nil
	}
	buf := make([]byte, sz)
	pMemcpy.Call(uintptr(unsafe.Pointer(&buf[0])), ptr, sz)
	return buf
}

func readDroppedFiles() ([]string, bool) {
	var paths []string
	clipThread(func() bool {
		if !openClipboard() {
			return false
		}
		defer pCloseClipboard.Call()
		h, _, _ := pGetClipboardDta.Call(cfHDrop)
		if h == 0 {
			return false
		}
		ptr, _, _ := pGlobalLock.Call(h)
		if ptr == 0 {
			return false
		}
		defer pGlobalUnlock.Call(h)
		paths = dropFiles(ptr)
		return true
	})
	return paths, paths != nil
}

func dropFiles(hDrop uintptr) []string {
	n, _, _ := pDragQueryFileW.Call(hDrop, ^uintptr(0), 0, 0)
	paths := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		buf := make([]uint16, 1024)
		pDragQueryFileW.Call(hDrop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		paths = append(paths, windows.UTF16ToString(buf))
	}
	return paths
}
