# tscp

SSH(SFTP) 文件上传 TUI 小工具：登录远程服务器，浏览并选择目标目录，把拖进终端窗口
（或复制到剪贴板后按 Ctrl+V）的本地文件上传过去。

## 用法

```
tscp -u root@10.10.10.3 -p password [-P 22]   # 密码登录
tscp -u root@10.10.10.3                        # 自动用 ~/.ssh 下的默认私钥
tscp                                           # 用缓存的上次目标 + 默认私钥
```

| 参数 | 说明 |
| ---- | ---- |
| `-u` | 目标机 `user@host`（user 省略时默认 root） |
| `-p` | 登录密码（与证书至少提供其一，两者都有时证书优先、密码兜底） |
| `-i` | 私钥文件；省略时自动尝试 `~/.ssh/id_ed25519`、`id_ecdsa`、`id_rsa` |
| `-P` | SSH 端口，默认 22 |

启动后先临时建立 SSH 连接执行命令校验（校验完即断开），然后进入 TUI。
运行期间不保持长连接：每次浏览目录、每批上传各自临时建连，用完即断。

## 目录缓存

`~/.tscp/cache.json` 会记录上次的目标机（`-u`）和每台机器上最后浏览的远程目录：

- `-u` 省略时自动使用上次的目标（含端口），此时只需 `tscp -p password`；
- 启动时直接进入该目标机上次浏览到的目录（目录已不存在则回家目录）；
- 删除 `~/.tscp/cache.json` 即可重置；登录密码不会缓存。
- 可用环境变量 `TSCP_CACHE_DIR` 覆盖缓存目录位置。

## npm / npx 分发

tscp 根目录本身就是 npm 包（`package.json` + `bin/tscp.js` 启动器 + `binaries/` 全平台
预编译二进制，Go 源码通过 `files` 白名单排除）。用户不需要装 Go。

主分发方式是 **GitHub 直跑**（`binaries/` 随仓库提交，不需要发 npm）：

```bash
# 发布前先重新编译所有平台(产物进 binaries/), 然后提交推送
build.cmd
git add . && git commit -m "update binaries" && git push

# 本地模拟用户流程验证
npx --yes -p . tscp -u root@10.10.10.3 -p password
```

可选：也可以发布到 npm registry。注意公共源上 `tscp` 名字已被占用(他人所有)，所以用带
scope 的名字 `@maocat/tscp`（scope 必须是自己的 npm 用户名或组织名；发布需账户开启 2FA）：

```bash
npm publish --access public     # 按 OTP 提示输入验证码
```

用户侧使用：

```bash
npx maocatooo/tscp -u root@10.10.10.3 -p password    # 从 GitHub 直接跑(推荐, 无需 npm)
npx @maocat/tscp -u root@10.10.10.3 -p password      # 或从 npm 跑
npm i -g @maocat/tscp                                # 全局安装后直接 tscp 命令
```

## 界面操作

| 按键 | 作用 |
| ---- | ---- |
| `↑/↓` | 移动光标 |
| `→` / `回车` | 进入远程目录 |
| `←` / `Backspace` | 返回上级 |
| `r` | 刷新目录 |
| 拖拽文件到终端窗口 | 终端把文件路径作为一串字符送入，程序自动识别加入上传队列 |
| 复制文件（资源管理器 `Ctrl+C`） | 状态栏提示"检测到 N 个文件"，按 `v` 加入队列（不会自动入队） |
| `Ctrl+V` / `v` | 读取剪贴板（文件列表或路径文本）加入队列 |
| `Tab` | 在目录列表 / 上传队列之间切换焦点 |
| `x` / `c` | 移除选中项 / 清空队列（焦点在队列时） |
| `u` | 开始上传：把队列中的文件上传到当前远程目录（带进度和速度） |
| `q` / `Ctrl+C` | 退出 |

拖入整个目录时按 `scp -r` 语义展开：保留目录名，远端自动创建子目录。

## 编译

```
cd tscp
go build .
```

> 本仓库 go.mod 要求 go >= 1.25（自动下载工具链）；如果设置了 `GOTOOLCHAIN=local`，
> 执行 `set GOTOOLCHAIN=auto` 后再编译。

## 说明与限制

- 主机指纹未校验（`InsecureIgnoreHostKey`），适合内网/测试环境；公网使用请自行加 known_hosts 校验。
- 认证：证书优先、密码兜底。不给 `-p` 时自动尝试 OpenSSH 默认私钥（`~/.ssh/id_ed25519`、
  `id_ecdsa`、`id_rsa`），也可用 `-i` 指定；带口令的私钥会在启动前提示输入（隐藏回显）。
  不支持 ssh-agent。密码通过命令行参数传入会留在 shell 历史里，介意就改用证书。
- 拖拽识别：Windows 终端（Windows Terminal / conhost）把拖入文件的路径作为一串按键字符送入，
  程序累积后整体识别（支持带引号、空格、多文件）；`Ctrl+V` 通常被终端拦截转为粘贴输入，
  同样会被识别为路径入队。
- 复制文件提示：程序每 0.5 秒检查一次剪贴板序号，在资源管理器里 `Ctrl+C` 复制文件后，状态栏
  会提示"检测到 N 个文件 — 按 v 加入队列"，**不会自动入队**；复制普通文本无任何提示。
- WSL：剪贴板通过 Windows interop 调 `powershell.exe Get-Clipboard` 读取（能拿到资源管理器
  复制的文件列表），Windows 路径自动转成 `/mnt/c/...`；拖拽进终端的 `/mnt/...` 路径同样可识别。
  需启用 WSL interop（默认启用）。复制文件的自动检测（轮询）在 WSL 不可用，按 `v` 手动读取。
- 上传走 SFTP，队列顺序逐个上传；同一批上传共用一条 SSH 连接，传完断开。
