//go:build windows

package tscp

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/atotto/clipboard"
	"golang.org/x/sys/windows"
)

// CF_HDROP: 资源管理器里 Ctrl+C 复制文件时剪贴板里的文件列表格式
const cfHDrop = 15

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	pOpenClipboard   = user32.NewProc("OpenClipboard")
	pCloseClipboard  = user32.NewProc("CloseClipboard")
	pIsFormatAvail   = user32.NewProc("IsClipboardFormatAvailable")
	pGetClipboardDta = user32.NewProc("GetClipboardData")
	pGlobalLock      = kernel32.NewProc("GlobalLock")
	pGlobalUnlock    = kernel32.NewProc("GlobalUnlock")
	pDragQueryFileW  = shell32.NewProc("DragQueryFileW")
	pClipboardSeqNum = user32.NewProc("GetClipboardSequenceNumber")
)

// clipboardSequence 返回剪贴板序号(内容每次变化 +1), 用于轮询检测"复制文件"
func clipboardSequence() uint32 {
	r, _, _ := pClipboardSeqNum.Call()
	return uint32(r)
}

// readClipboardPaths 读剪贴板: 优先取复制的文件列表(CF_HDROP), 其次把文本按行解析为路径
func readClipboardPaths() ([]string, error) {
	if r, _, _ := pIsFormatAvail.Call(cfHDrop); r != 0 {
		if paths, ok := readDroppedFiles(); ok {
			return paths, nil
		}
	}
	text, err := clipboard.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取剪贴板失败: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

func readDroppedFiles() ([]string, bool) {
	if r, _, _ := pOpenClipboard.Call(0); r == 0 {
		return nil, false
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardDta.Call(cfHDrop)
	if h == 0 {
		return nil, false
	}
	ptr, _, _ := pGlobalLock.Call(h)
	if ptr == 0 {
		return nil, false
	}
	defer pGlobalUnlock.Call(h)
	return dropFiles(ptr), true
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
