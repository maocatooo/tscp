package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	tscp "github.com/maocatooo/tscp/src"
)

func usage() {
	fmt.Fprint(os.Stderr, `tscp - SSH(SFTP) 文件上传 TUI

用法:
  tscp -u root@10.10.10.3 -p password [-P 22]

参数:
  -u  目标机, 格式 user@host (user 省略时默认 root; 整体省略时用上次的目标)
  -p  登录密码, 与证书至少提供其一
  -i  私钥文件; 省略时自动尝试 ~/.ssh/id_ed25519, id_ecdsa, id_rsa
  -P  SSH 端口, 默认 22

缓存:
  ~/.tscp/cache.json 记录上次的目标机和浏览目录, 下次启动自动进入;
  删除该文件即可重置。密码不会缓存。

进入 TUI 后:
  ↑↓ 选择远程目录, →/回车 进入, ← 返回, r 刷新
  把文件拖进终端窗口, 或复制文件后按 Ctrl+V / v 添加到上传队列
  u 开始上传到当前远程目录, q 退出
`)
}

func die(format string, a ...any) {
	fmt.Fprintln(os.Stderr, "错误: "+fmt.Sprintf(format, a...))
	os.Exit(1)
}

func main() {
	var (
		userHost = flag.String("u", "", "user@host (user 省略时默认 root; 整体省略时用上次的目标)")
		password = flag.String("p", "", "登录密码(与证书至少其一, 证书优先)")
		identity = flag.String("i", "", "私钥文件 (省略时自动尝试 ~/.ssh/id_ed25519 等默认私钥)")
		portFlag = flag.Int("P", 0, "SSH 端口, 默认 22")
	)
	flag.Usage = usage
	flag.Parse()

	cache := tscp.LoadCache()
	user, host, port := "root", "", 22
	if *userHost != "" {
		if u, h, ok := strings.Cut(*userHost, "@"); ok {
			user, host = u, h
		} else {
			host = *userHost
		}
	} else if cu, ch, cp, ok := tscp.ParseCachedTarget(cache.Last); ok {
		user, host, port = cu, ch, cp
		fmt.Fprintf(os.Stderr, "使用上次目标: %s\n", cache.Last)
	} else {
		usage()
		die("-u 不能为空, 且没有可用的历史目标")
	}
	if host == "" {
		die("-u 格式应为 user@host")
	}
	if *portFlag != 0 {
		port = *portFlag
	}
	key := tscp.CacheKey(user, host, port)

	fmt.Fprintf(os.Stderr, "连接 %s@%s:%d …\n", user, host, port)
	// 临时建连: 组装认证方式(证书/密码) + SSH 执行命令校验 + 取家目录, 校验完即断开
	r, home, err := tscp.ConnectAndValidate(user, host, port, *password, *identity, 10*time.Second)
	if err != nil {
		die("%v", err)
	}

	// 上次浏览过的目录仍存在就直接进入, 否则回家目录
	startDir := home
	if d := cache.Dirs[key]; d != "" {
		if _, err := r.ListDir(d); err == nil {
			startDir = d
		}
	}

	p := tea.NewProgram(tscp.NewModel(r, startDir, cache, key), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		die("TUI 运行失败: %v", err)
	}
}
