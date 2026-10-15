package tscp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// Entry 是远程目录里的一项
type Entry struct {
	Name  string
	Size  int64
	IsDir bool
}

// Remote 只保存连接参数, 不持有长连接:
// 启动校验、每次目录浏览、每批上传各自临时建连, 用完即断
type Remote struct {
	Host    string
	User    string
	port    int
	auth    []ssh.AuthMethod
	timeout time.Duration
}

// batch 是一条临时连接(一次目录浏览或一批上传共用)
type batch struct {
	sshClient *ssh.Client
	sftp      *sftp.Client
}

func (r *Remote) dial() (*batch, error) {
	cfg := &ssh.ClientConfig{
		User: r.User,
		Auth: r.auth,
		// 内网传文件工具: 不校验主机指纹
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         r.timeout,
	}
	c, err := ssh.Dial("tcp", net.JoinHostPort(r.Host, strconv.Itoa(r.port)), cfg)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	sc, err := sftp.NewClient(c, sftp.UseConcurrentWrites(true))
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("打开 SFTP 会话失败: %w", err)
	}
	return &batch{sshClient: c, sftp: sc}, nil
}

func (b *batch) close() {
	b.sftp.Close()
	b.sshClient.Close()
}

// ConnectAndValidate 临时建连: SSH 执行命令校验 + 取家目录, 完成后立即断开
func ConnectAndValidate(user, host string, port int, password, identity string, timeout time.Duration) (*Remote, string, error) {
	auth, err := authMethods(password, identity)
	if err != nil {
		return nil, "", err
	}
	r := &Remote{Host: host, User: user, port: port, auth: auth, timeout: timeout}
	b, err := r.dial()
	if err != nil {
		return nil, "", err
	}
	defer b.close()

	out, err := b.runCmd("echo tscp-ok")
	if err != nil {
		return nil, "", fmt.Errorf("SSH 命令执行校验失败: %w", err)
	}
	if !strings.Contains(out, "tscp-ok") {
		return nil, "", fmt.Errorf("SSH 命令执行校验失败: 服务器返回异常: %q", strings.TrimSpace(out))
	}
	home, err := b.sftp.RealPath(".")
	if err != nil || home == "" {
		home = "/"
	}
	return r, home, nil
}

// defaultIdentityFiles 是自动尝试的默认私钥, 与 OpenSSH 一致
func defaultIdentityFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
		filepath.Join(home, ".ssh", "id_rsa"),
	}
}

// authMethods 组装认证方式: 证书(显式 -i 优先, 否则默认证书)在前, 密码兜底
func authMethods(password, identity string) ([]ssh.AuthMethod, error) {
	files := defaultIdentityFiles()
	if identity != "" {
		files = []string{identity}
	}
	var methods []ssh.AuthMethod
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue // 不存在/不可读就跳过, 与 ssh 行为一致
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			var pp *ssh.PassphraseMissingError
			if errors.As(err, &pp) {
				fmt.Fprintf(os.Stderr, "私钥 %s 有口令保护, 请输入口令: ", f)
				if pass, perr := readSecret(); perr == nil {
					signer, err = ssh.ParsePrivateKeyWithPassphrase(data, pass)
				}
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "跳过私钥 %s: %v\n", f, err)
				continue
			}
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	switch {
	case len(methods) == 0 && identity != "":
		return nil, fmt.Errorf("无法使用指定的私钥 %s", identity)
	case len(methods) == 0:
		return nil, fmt.Errorf("没有可用的认证方式: 请提供 -p 密码, 或准备默认私钥 ~/.ssh/id_ed25519|id_ecdsa|id_rsa, 或用 -i 指定私钥")
	}
	return methods, nil
}

// readSecret 从终端读一段隐藏输入(私钥口令), 非终端时退化为明文读一行
func readSecret() ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return b, err
	}
	line, _, err := bufio.NewReader(os.Stdin).ReadLine()
	return line, err
}

// runCmd 通过 SSH 执行一条命令, 返回合并后的输出
func (b *batch) runCmd(cmd string) (string, error) {
	sess, err := b.sshClient.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

// ListDir 临时建连列出远程目录, 目录排在前面, 各自按名字排序
func (r *Remote) ListDir(dir string) ([]Entry, error) {
	b, err := r.dial()
	if err != nil {
		return nil, err
	}
	defer b.close()

	fis, err := b.sftp.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(fis))
	for _, fi := range fis {
		entries = append(entries, Entry{Name: fi.Name(), Size: fi.Size(), IsDir: fi.IsDir()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// OpenBatch 为一批上传建立一条临时连接, 整批结束后由调用方 Close
func (r *Remote) OpenBatch() (*batch, error) {
	return r.dial()
}

// MkdirAll 递归创建远程目录(已存在的跳过)
func (b *batch) MkdirAll(dir string) error {
	dir = path.Clean(dir)
	if !strings.HasPrefix(dir, "/") {
		dir = "/" + dir
	}
	cur := ""
	for _, part := range strings.Split(dir, "/") {
		if part == "" {
			continue
		}
		cur = cur + "/" + part
		if _, err := b.sftp.Stat(cur); err == nil {
			continue
		}
		if err := b.sftp.Mkdir(cur); err != nil {
			if _, serr := b.sftp.Stat(cur); serr != nil {
				return fmt.Errorf("创建远程目录 %s: %w", cur, err)
			}
		}
	}
	return nil
}

// Upload 把本地文件流式写入远端, prog 回调上报进度
func (b *batch) Upload(local, remote string, prog func(sent, total int64)) error {
	st, err := os.Stat(local)
	if err != nil {
		return err
	}
	lf, err := os.Open(local)
	if err != nil {
		return err
	}
	defer lf.Close()

	rf, err := b.sftp.Create(remote)
	if err != nil {
		return fmt.Errorf("创建远程文件: %w", err)
	}

	buf := make([]byte, 128*1024)
	var sent int64
	if prog != nil {
		prog(0, st.Size())
	}
	for {
		n, rerr := lf.Read(buf)
		if n > 0 {
			if _, werr := rf.Write(buf[:n]); werr != nil {
				rf.Close()
				return fmt.Errorf("写入远端: %w", werr)
			}
			sent += int64(n)
			if prog != nil {
				prog(sent, st.Size())
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			rf.Close()
			return rerr
		}
	}
	if err := rf.Close(); err != nil {
		return fmt.Errorf("关闭远程文件: %w", err)
	}
	return nil
}
