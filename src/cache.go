package tscp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// tscpCache 记录上次的目标机与浏览目录, 存于 ~/.tscp/cache.json
type tscpCache struct {
	Last string            `json:"last"` // 上次目标, 格式 user@host:port
	Dirs map[string]string `json:"dirs"` // user@host:port -> 上次浏览的远程目录
}

var cacheMu sync.Mutex

// cacheDir 返回缓存目录(每次启动确保存在); 可用环境变量 TSCP_CACHE_DIR 覆盖
func cacheDir() string {
	if v := os.Getenv("TSCP_CACHE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".tscp")
	}
	return filepath.Join(home, ".tscp")
}

func cachePath() string {
	return filepath.Join(cacheDir(), "cache.json")
}

// LoadCache 读取缓存, 目录不存在则创建; 任何异常都返回空缓存, 不影响启动
func LoadCache() *tscpCache {
	_ = os.MkdirAll(cacheDir(), 0o755)
	c := &tscpCache{Dirs: map[string]string{}}
	data, err := os.ReadFile(cachePath())
	if err != nil {
		return c
	}
	if err := json.Unmarshal(data, c); err != nil {
		return &tscpCache{Dirs: map[string]string{}}
	}
	if c.Dirs == nil {
		c.Dirs = map[string]string{}
	}
	return c
}

// saveCache 原子写回缓存文件
func saveCache(c *tscpCache) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	tmp := cachePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, cachePath())
}

func CacheKey(user, host string, port int) string {
	return fmt.Sprintf("%s@%s:%d", user, host, port)
}

// ParseCachedTarget 解析缓存里的 "user@host:port"
func ParseCachedTarget(s string) (user, host string, port int, ok bool) {
	port = 22
	rest := s
	if i := strings.LastIndex(s, ":"); i >= 0 {
		if v, err := strconv.Atoi(s[i+1:]); err == nil && v > 0 {
			port, rest = v, s[:i]
		}
	}
	user, host = "root", rest
	if u, h, found := strings.Cut(rest, "@"); found {
		user, host = u, h
	}
	return user, host, port, host != ""
}
