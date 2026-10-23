package tscp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// isWSL 判断当前是否运行在 WSL 里(环境变量或 /proc/version 标记)
func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// winToWSLPath 把 Windows 路径 C:\a\b 转成 WSL 路径 /mnt/c/a/b; 非此形态原样返回
func winToWSLPath(p string) string {
	if len(p) < 3 || p[1] != ':' {
		return p
	}
	drive := strings.ToLower(p[:1])
	rest := strings.ReplaceAll(p[2:], "\\", "/")
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	return "/mnt/" + drive + rest
}

// saveScreenshotTmp 把剪贴板截图写进临时文件, 作为普通文件进入上传队列
func saveScreenshotTmp(data []byte, ext string) (string, error) {
	dir := filepath.Join(os.TempDir(), "tscp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("screenshot_%s.%s", time.Now().Format("20060102_150405"), ext))
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}
