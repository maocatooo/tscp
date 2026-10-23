//go:build !windows

package tscp

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

// clipboardSequence 非 Windows 平台不支持轮询, 恒返回 0
// (WSL 里轮询需要反复拉起 powershell, 代价太高, 改为按 v 手动读取)
func clipboardSequence() uint32 { return 0 }

// readClipboardPaths 读剪贴板并按行解析为路径
// WSL 优先通过 interop 调 Windows 侧 powershell.exe(能读到复制的文件列表);
// 其余平台用 pbpaste / xclip / xsel
func readClipboardPaths() ([]string, error) {
	if isWSL() {
		if paths, ok := readWSLClipboard(); ok {
			return paths, nil
		}
	}
	text, err := clipboard.ReadAll()
	if err != nil {
		if isWSL() {
			return nil, errWSLClipboard
		}
		return nil, err
	}
	return parsePathLines(text), nil
}

// peekClipboard 无副作用探测剪贴板(非 Windows 平台无轮询, 仅文本)
func peekClipboard() (paths []string, image bool) {
	if text, err := clipboard.ReadAll(); err == nil {
		return parsePathLines(text), false
	}
	return nil, false
}

var errWSLClipboard = wslClipboardError{}

type wslClipboardError struct{}

func (wslClipboardError) Error() string {
	return "WSL 里读剪贴板失败: 需要 Windows interop(powershell.exe), 或安装 xclip/xsel"
}

// readWSLClipboard 通过 powershell.exe 读 Windows 剪贴板:
// 先取文件列表(资源管理器 Ctrl+C 的内容), 再取截图, 最后取文本
func readWSLClipboard() ([]string, bool) {
	ps := findPowerShell()
	if ps == "" {
		return nil, false
	}
	if out, err := runPS(ps, "(Get-Clipboard -Format FileDropList).FullName"); err == nil {
		if paths := parsePathLines(out); len(paths) > 0 {
			return paths, true
		}
	}
	// 截图: 让 Windows 侧把剪贴板图片存成 PNG 临时文件, 再转成 WSL 路径
	if out, err := runPS(ps, `try{$p=Join-Path $env:TEMP 'tscp_screenshot.png';(Get-Clipboard -Format Image).Save($p,[System.Drawing.Imaging.ImageFormat]::Png);Write-Output $p}catch{}`); err == nil {
		if p := firstLine(out); p != "" {
			return []string{winToWSLPath(p)}, true
		}
	}
	if out, err := runPS(ps, "Get-Clipboard"); err == nil {
		return parsePathLines(out), true
	}
	return nil, false
}

// firstLine 取第一行非空文本
func firstLine(text string) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// findPowerShell 找到 WSL interop 的 powershell.exe
func findPowerShell() string {
	for _, c := range []string{
		"powershell.exe",
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe",
	} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
}

func runPS(ps, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; "+script)
	out, err := cmd.Output()
	return string(out), err
}

// parsePathLines 把多行文本解析为非空路径行, Windows 风格路径转成 /mnt/ 形式
func parsePathLines(text string) []string {
	var paths []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, winToWSLPath(line))
		}
	}
	return paths
}
