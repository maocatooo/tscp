package tscp

import (
	"os"
	"strings"
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
