package tscp

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// ---- 上传队列项状态 ----

const (
	stWait = iota
	stRun
	stDone
	stFail
)

type queueItem struct {
	id     int
	local  string
	remote string
	size   int64
	sent   int64
	status int
	errMsg string
}

func (it queueItem) statusStyled() string {
	switch it.status {
	case stRun:
		pct := 0.0
		if it.size > 0 {
			pct = float64(it.sent) / float64(it.size)
		}
		return stWarn.Render(fmt.Sprintf("↑%3.0f%%", pct*100))
	case stDone:
		return stOK.Render("✔ 完成")
	case stFail:
		return stBad.Render("✘ " + it.errMsg)
	default:
		return stDim.Render("等待")
	}
}

type focusZone int

const (
	focusDir focusZone = iota
	focusQueue
)

// ---- 消息 ----

type dirLoadedMsg struct {
	dir     string
	entries []Entry
	err     error
}

type queueMsg struct {
	items  []queueItem
	notice string
}

type uploadMsg struct {
	id    int
	sent  int64
	total int64
	done  bool
	err   error
}

type uploadFinishedMsg struct{}

type dropSettleMsg struct{} // 按键字符流已静止, 可以尝试识别路径

type clipPollMsg struct{ seq uint32 } // 剪贴板序号轮询

type clipHintMsg struct{ paths []string } // 剪贴板里检测到的文件(仅提示, 不自动入队)

// ---- 样式 ----

var (
	stTitle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	stDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	stDirItem   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	stOK        = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stBad       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stWarn      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stBorderOn  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62"))
	stBorderOff = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238"))
)

// ---- Model ----

type model struct {
	r      *Remote
	width  int
	height int

	cwd     string
	entries []Entry
	cursor  int

	queue   []queueItem
	qcursor int
	nextID  int

	focus     focusZone
	uploading bool
	ch        chan tea.Msg
	progress  progress.Model

	speed    float64 // 当前文件的上传速度 bytes/s
	lastID   int
	lastSent int64
	lastAt   time.Time

	// 拖拽/粘贴识别: Windows 终端把粘贴内容作为一串字符按键送入, 先累积再判断
	pasteBuf      string
	pasteLast     time.Time
	pasteSuppress bool // 字符流已呈现路径特征, 期间拦截单字母快捷键

	clipSeq uint32 // 上次见到的剪贴板序号, 轮询检测"复制文件"

	cache    *tscpCache // 上次目录缓存(可为 nil, 如测试)
	cacheKey string     // 当前目标 user@host:port

	status string
}

func NewModel(r *Remote, startDir string, cache *tscpCache, key string) model {
	return model{
		r:        r,
		cwd:      startDir,
		progress: progress.New(progress.WithDefaultGradient()),
		status:   "目录加载中…",
		lastID:   -1,
		clipSeq:  clipboardSequence(),
		cache:    cache,
		cacheKey: key,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.cdCmd(m.cwd), pollClipboardCmd(m.clipSeq))
}

func (m model) cdCmd(dir string) tea.Cmd {
	r := m.r
	return func() tea.Msg {
		entries, err := r.ListDir(dir)
		return dirLoadedMsg{dir: dir, entries: entries, err: err}
	}
}

func (m model) listenCh() tea.Cmd {
	ch := m.ch
	if ch == nil {
		return nil
	}
	return func() tea.Msg { return <-ch }
}

func (m *model) indexOf(id int) int {
	for i := range m.queue {
		if m.queue[i].id == id {
			return i
		}
	}
	return -1
}

// ---- Update ----

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.progress.Width = max(10, msg.Width-6)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case dirLoadedMsg:
		if msg.err != nil {
			m.status = "读取目录失败: " + msg.err.Error()
			return m, nil
		}
		// 上方栏只列目录, 不显示文件
		dirs := make([]Entry, 0, len(msg.entries))
		for _, e := range msg.entries {
			if e.IsDir {
				dirs = append(dirs, e)
			}
		}
		m.cwd, m.entries, m.cursor = msg.dir, dirs, 0
		m.status = "已就绪"
		// 记住这台机器上浏览到的目录, 下次启动直接进入
		if m.cache != nil && m.cacheKey != "" {
			m.cache.Dirs[m.cacheKey] = msg.dir
			m.cache.Last = m.cacheKey
			saveCache(m.cache)
		}

	case queueMsg:
		added := 0
		for i := range msg.items {
			dup := false
			for _, ex := range m.queue {
				if ex.local == msg.items[i].local && ex.remote == msg.items[i].remote && ex.status != stFail {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			msg.items[i].id = m.nextID
			msg.items[i].size = fileSize(msg.items[i].local)
			m.nextID++
			m.queue = append(m.queue, msg.items[i])
			added++
		}
		if added > 0 {
			m.status = fmt.Sprintf("已添加 %d 个文件, 上传目标目录 %s (按 u 开始)", added, m.cwd)
		}
		if msg.notice != "" {
			if added > 0 {
				m.status += " | " + msg.notice
			} else {
				m.status = msg.notice
			}
		}

	case uploadMsg:
		return m.handleUploadMsg(msg)

	case uploadFinishedMsg:
		m.uploading = false
		m.status = "上传结束, 目录已刷新"
		return m, m.cdCmd(m.cwd)

	case dropSettleMsg:
		if m.pasteBuf != "" && time.Since(m.pasteLast) >= 400*time.Millisecond {
			return m.flushPasteBuf()
		}
		return m, nil

	case clipPollMsg:
		next := pollClipboardCmd(msg.seq)
		if msg.seq == 0 || msg.seq == m.clipSeq {
			return m, next
		}
		// 剪贴板变了: 若里面是文件/存在的路径, 只提示, 由用户按 v 决定是否入队
		m.clipSeq = msg.seq
		return m, tea.Batch(next, func() tea.Msg {
			paths, err := readClipboardPaths()
			if err != nil {
				return clipHintMsg{}
			}
			return clipHintMsg{paths: filterExisting(paths)}
		})

	case clipHintMsg:
		if len(msg.paths) > 0 {
			name := filepath.Base(msg.paths[0])
			if r := []rune(name); len(r) > 40 {
				name = string(r[:40]) + "…"
			}
			m.status = fmt.Sprintf("检测到复制的文件 %d 个, 首个: %s — 按 v 加入队列", len(msg.paths), name)
		}

	case progress.FrameMsg:
		pm, cmd := m.progress.Update(msg)
		m.progress = pm.(progress.Model)
		return m, cmd
	}
	return m, nil
}

func (m model) handleUploadMsg(msg uploadMsg) (tea.Model, tea.Cmd) {
	idx := m.indexOf(msg.id)
	if idx >= 0 {
		it := &m.queue[idx]
		if msg.done {
			if msg.err != nil {
				it.status = stFail
				it.errMsg = msg.err.Error()
				m.status = "上传失败: " + path.Base(it.local) + " " + msg.err.Error()
			} else {
				it.status = stDone
				it.sent = it.size
			}
		} else {
			it.status = stRun
			it.sent, it.size = msg.sent, msg.total
			now := time.Now()
			switch {
			case m.lastID != msg.id:
				m.lastID, m.lastSent, m.lastAt, m.speed = msg.id, msg.sent, now, 0
			case now.Sub(m.lastAt) > 300*time.Millisecond:
				m.speed = float64(msg.sent-m.lastSent) / now.Sub(m.lastAt).Seconds()
				m.lastSent, m.lastAt = msg.sent, now
			}
		}
	}
	var cmds []tea.Cmd
	if idx >= 0 && m.queue[idx].size > 0 {
		pct := float64(m.queue[idx].sent) / float64(m.queue[idx].size)
		cmds = append(cmds, m.progress.SetPercent(pct))
	}
	cmds = append(cmds, m.listenCh())
	return m, tea.Batch(cmds...)
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// bracketed paste(非 Windows 终端): 一次性收到路径文本
	if msg.Paste {
		paths := extractPaths(sanitizePastedText(string(msg.Runes)))
		if len(paths) == 0 {
			m.status = "粘贴内容里没有识别到文件路径"
			return m, nil
		}
		cwd := m.cwd
		return m, func() tea.Msg { return buildQueueMsg(paths, cwd) }
	}

	now := time.Now()
	burst := now.Sub(m.pasteLast) < 500*time.Millisecond

	// 拖拽文件/Ctrl+V 在 Windows 上表现为一串字符按键: 累积, 静止后整体识别
	if m.pasteSuppress && burst {
		switch msg.String() {
		case "enter", "ctrl+m": // 某些终端粘贴会带回车, 直接结算
			m.pasteBuf += "\n"
			return m.flushPasteBuf()
		case "esc": // 括号粘贴标记可能转成 esc 按键, 吞掉
			return m, nil
		}
	}
	if msg.Type == tea.KeyRunes {
		s := sanitizePastedText(string(msg.Runes))
		if s != "" {
			cand := m.pasteBuf + s
			switch {
			case (burst && pathCommitted(cand)) || m.pasteSuppress ||
				(!burst && pathCommitted(cand) && !bindingKeys[s]):
				m.pasteBuf, m.pasteLast, m.pasteSuppress = cand, now, true
				return m, dropSettleCmd()
			case !burst && !bindingKeys[s]:
				// 无关字母先记下(后续可能拼成路径), 但不拦截快捷键
				m.pasteBuf, m.pasteLast, m.pasteSuppress = s, now, false
				return m, dropSettleCmd()
			}
			m.pasteBuf, m.pasteSuppress = "", false
		}
	}

	k := msg.String()
	switch k {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "ctrl+v", "v":
		cwd := m.cwd
		return m, func() tea.Msg {
			paths, err := readClipboardPaths()
			if err != nil {
				return queueMsg{notice: "读取剪贴板失败: " + err.Error()}
			}
			if len(paths) == 0 {
				return queueMsg{notice: "剪贴板里没有文件或路径"}
			}
			return buildQueueMsg(paths, cwd)
		}

	case "tab", "shift+tab":
		if m.focus == focusDir {
			m.focus = focusQueue
		} else {
			m.focus = focusDir
		}
		m.qcursor = clamp(m.qcursor, 0, max(0, len(m.queue)-1))
		return m, nil

	case "u":
		cmd := m.startUpload()
		return m, cmd

	case "c":
		if m.uploading {
			m.status = "上传中不能清空队列"
		} else {
			m.queue, m.qcursor = nil, 0
			m.status = "队列已清空"
		}
		return m, nil
	}

	if m.focus == focusDir {
		return m.dirKeys(k)
	}
	return m.queueKeys(k)
}

func (m model) dirKeys(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = len(m.entries)
	case "right", "l", "enter":
		return m.enterDir()
	case "left", "h", "backspace":
		if m.cwd != "/" {
			return m, m.cdCmd(path.Dir(m.cwd))
		}
	case "r":
		return m, m.cdCmd(m.cwd)
	}
	m.cursor = clamp(m.cursor, 0, len(m.entries)) // +1 是固定首行 ".."
	return m, nil
}

func (m model) enterDir() (tea.Model, tea.Cmd) {
	if m.cursor == 0 {
		return m, m.cdCmd(path.Dir(m.cwd)) // ".."
	}
	if m.cursor-1 >= len(m.entries) {
		return m, nil
	}
	e := m.entries[m.cursor-1]
	return m, m.cdCmd(path.Join(m.cwd, e.Name))
}

func (m model) queueKeys(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		m.qcursor--
	case "down", "j":
		m.qcursor++
	case "x", "delete":
		if len(m.queue) == 0 {
			return m, nil
		}
		i := clamp(m.qcursor, 0, len(m.queue)-1)
		if m.queue[i].status == stRun {
			m.status = "该文件正在上传, 不能移除"
			return m, nil
		}
		m.queue = append(m.queue[:i], m.queue[i+1:]...)
		if len(m.queue) == 0 {
			m.focus = focusDir
		}
		m.qcursor = clamp(m.qcursor, 0, max(0, len(m.queue)-1))
		return m, nil
	}
	m.qcursor = clamp(m.qcursor, 0, max(0, len(m.queue)-1))
	return m, nil
}

// startUpload 把队列里所有等待项顺序上传到各自 remote 路径
func (m *model) startUpload() tea.Cmd {
	if m.uploading {
		m.status = "正在上传中…"
		return nil
	}
	var pend []queueItem
	for _, it := range m.queue {
		if it.status == stWait {
			pend = append(pend, it)
		}
	}
	if len(pend) == 0 {
		m.status = "队列是空的, 先拖拽文件到终端窗口或按 Ctrl+V/v 添加"
		return nil
	}
	m.uploading = true
	m.status = fmt.Sprintf("开始上传 %d 个文件到 %s", len(pend), m.cwd)

	ch := make(chan tea.Msg, 256)
	m.ch = ch
	r := m.r
	go func() {
		// 一批上传共用一条临时连接, 传完即断
		bt, err := r.OpenBatch()
		if err != nil {
			for _, it := range pend {
				ch <- uploadMsg{id: it.id, done: true, err: err}
			}
			ch <- uploadFinishedMsg{}
			return
		}
		defer bt.close()
		for _, it := range pend {
			uerr := bt.MkdirAll(path.Dir(it.remote))
			if uerr == nil {
				uerr = bt.Upload(it.local, it.remote, func(sent, total int64) {
					select {
					case ch <- uploadMsg{id: it.id, sent: sent, total: total}:
					default: // 进度是可丢弃的, 不阻塞上传
					}
				})
			}
			ch <- uploadMsg{id: it.id, done: true, err: uerr}
		}
		ch <- uploadFinishedMsg{}
	}()
	return tea.Batch(m.listenCh())
}

// ---- 拖拽/粘贴识别 ----

// bindingKeys 是会被 TUI 消费的单字母按键; 其余字母允许进入粘贴累积缓冲
var bindingKeys = map[string]bool{
	"u": true, "c": true, "v": true, "q": true, "g": true, "G": true,
	"j": true, "k": true, "h": true, "l": true, "x": true, "r": true,
}

// pathCommitted 判断已累积的字符是否呈现本地路径特征(盘符冒号/反斜杠/引号/斜杠开头)
func pathCommitted(s string) bool {
	return strings.Contains(s, ":") || strings.Contains(s, `\`) ||
		strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "/")
}

// looksLikePathText 判断整段文本是否像拖拽/粘贴进来的本地路径
// (Windows 的 X:\ 与 UNC, 以及 Linux/macOS/WSL 的 / 开头绝对路径)
func looksLikePathText(s string) bool {
	if strings.HasPrefix(s, "/") || strings.Contains(s, `"/`) {
		return true
	}
	return strings.Contains(s, `:\`) || strings.Contains(s, `\\`) ||
		(strings.Contains(s, `"`) && strings.Contains(s, `\`))
}

var pasteSanitizer = strings.NewReplacer("\x1b", "", "[200~", "", "[201~", "")

// sanitizePastedText 去掉可能混入输入流的括号粘贴标记
func sanitizePastedText(s string) string {
	return pasteSanitizer.Replace(s)
}

// flushPasteBuf 把累积的字符流尝试识别为本地路径并加入队列
func (m model) flushPasteBuf() (tea.Model, tea.Cmd) {
	buf := m.pasteBuf
	m.pasteBuf, m.pasteSuppress, m.pasteLast = "", false, time.Time{}
	if buf == "" || !looksLikePathText(buf) {
		return m, nil
	}
	paths := extractPaths(buf)
	if len(paths) == 0 {
		return m, nil
	}
	cwd := m.cwd
	return m, func() tea.Msg { return buildQueueMsg(paths, cwd) }
}

func dropSettleCmd() tea.Cmd {
	return tea.Tick(450*time.Millisecond, func(time.Time) tea.Msg { return dropSettleMsg{} })
}

func pollClipboardCmd(seq uint32) tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return clipPollMsg{seq: clipboardSequence()}
	})
}

// filterExisting 只保留真实存在的本地路径(剪贴板自动检测用, 普通文本静默忽略)
func filterExisting(paths []string) []string {
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"'`))
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// ---- 队列构建 ----

// extractPaths 从粘贴文本里解析本地路径: 支持多行、引号包裹、一行多个带引号的路径
func extractPaths(text string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		found := false
		for _, q := range []string{`"`, `'`} {
			parts := strings.Split(line, q)
			for i := 1; i < len(parts); i += 2 {
				if s := strings.TrimSpace(parts[i]); s != "" {
					out = append(out, s)
					found = true
				}
			}
		}
		if !found {
			out = append(out, line)
		}
	}
	return out
}

// buildQueueMsg 把路径列表转成队列项: 文件直接入队, 目录按相对结构展开入队
func buildQueueMsg(paths []string, cwd string) queueMsg {
	var items []queueItem
	var skips []string
	for _, p := range paths {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"'`))
		if p == "" {
			continue
		}
		// Windows 上 / 开头的粘贴内容更像远程路径, 跳过;
		// Linux/macOS/WSL 上 / 开头正是本地绝对路径, 交给 os.Stat 判断
		if runtime.GOOS == "windows" && strings.HasPrefix(p, "/") {
			skips = append(skips, "跳过远程路径: "+p)
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			skips = append(skips, "不存在: "+p)
			continue
		}
		if st.IsDir() {
			root := filepath.Dir(p)
			_ = filepath.WalkDir(p, func(fp string, d fs.DirEntry, werr error) error {
				if werr != nil || !d.Type().IsRegular() {
					return nil
				}
				rel, _ := filepath.Rel(root, fp)
				items = append(items, queueItem{
					local:  fp,
					remote: path.Join(cwd, filepath.ToSlash(rel)),
				})
				return nil
			})
		} else {
			items = append(items, queueItem{
				local:  p,
				remote: path.Join(cwd, path.Base(filepath.ToSlash(p))),
			})
		}
	}
	return queueMsg{items: items, notice: strings.Join(skips, "; ")}
}

// ---- View ----

func (m model) View() string {
	if m.width == 0 {
		return "tscp 启动中…"
	}
	w, h := m.width, m.height

	header := stOK.Render("●") + stTitle.Render(fmt.Sprintf(" tscp ▸ %s@%s", m.r.User, m.r.Host)) +
		stDim.Render("   上传目标目录: "+m.cwd)

	footH := 2
	if m.uploading {
		footH = 3
	}
	bodyH := max(8, h-1-footH)
	dirH := max(5, bodyH*2/5)
	queueH := max(4, bodyH-dirH)
	innerW := max(10, w-2)

	var foot []string
	if m.uploading {
		foot = append(foot, m.progressLine(w))
	}
	foot = append(foot, " "+truncate.StringWithTail(m.status, uint(max(1, w-1)), "…"))
	foot = append(foot, stDim.Render(" 拖拽/粘贴路径 · 复制文件后按 v 入队 · u 上传 · Tab 切换 · q 退出  |  目录: →进入 ←返回 r 刷新 · 队列: ↑↓ x 移除 c 清空"))

	return lipgloss.JoinVertical(lipgloss.Left, header,
		m.dirView(innerW, dirH-2),
		m.queueView(innerW, queueH-2),
		strings.Join(foot, "\n"))
}

func (m model) progressLine(w int) string {
	var cur *queueItem
	for i := range m.queue {
		if m.queue[i].status == stRun {
			cur = &m.queue[i]
			break
		}
	}
	if cur == nil {
		return ""
	}
	pct := 0.0
	if cur.size > 0 {
		pct = float64(cur.sent) / float64(cur.size)
	}
	label := stWarn.Render(fmt.Sprintf(" ↑ %s %.0f%% %s/s", path.Base(cur.local), pct*100, humanBytes(int64(m.speed))))
	barW := w - lipgloss.Width(label) - 2
	if barW < 10 {
		return label
	}
	m.progress.Width = barW
	return label + " " + m.progress.ViewAs(pct)
}

func (m model) dirView(innerW, n int) string {
	if n < 1 {
		n = 1
	}
	rows := make([]Entry, 0, len(m.entries)+1)
	rows = append(rows, Entry{Name: "..", IsDir: true})
	rows = append(rows, m.entries...)
	total := len(rows)

	cursor := clamp(m.cursor, 0, total-1)
	start := 0
	if total > n {
		start = clamp(cursor-n+1, 0, total-n)
	}

	var lines []string
	for i := start; i < min(total, start+n); i++ {
		mark := "  "
		if i == cursor {
			mark = stTitle.Render("▸ ")
		}
		lines = append(lines, fitRow(mark+stDirItem.Render(rows[i].Name+"/"), "", innerW))
	}
	if total == 1 && n > 1 {
		lines = append(lines, stDim.Render("  (无子目录)"))
	}
	for len(lines) < n {
		lines = append(lines, strings.Repeat(" ", innerW))
	}

	style := stBorderOff
	if m.focus == focusDir {
		style = stBorderOn
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m model) queueView(innerW, n int) string {
	if n < 1 {
		n = 1
	}
	var lines []string

	if len(m.queue) == 0 {
		lines = append(lines, stDim.Render(" (空) 拖拽文件 / 粘贴路径 / 复制文件后按 v, 均可加入队列"+strings.Repeat(" ", 4)))
	} else {
		done, fail := 0, 0
		for _, it := range m.queue {
			if it.status == stDone {
				done++
			}
			if it.status == stFail {
				fail++
			}
		}
		lines = append(lines, stDim.Render(fmt.Sprintf(" %d 项 · ✔ %d · ✘ %d", len(m.queue), done, fail)))
	}

	rows := m.queue
	total := len(rows)
	if total > 0 {
		qc := clamp(m.qcursor, 0, total-1)
		start := 0
		if total > n {
			start = clamp(qc-n+1, 0, total-n)
		}
		for i := start; i < min(total, start+n); i++ {
			it := rows[i]
			mark := "  "
			if i == qc {
				mark = stTitle.Render("▸ ")
			}
			left := mark + filepath.Base(it.local) + "  " + stDim.Render("→ "+it.remote)
			lines = append(lines, fitRow(left, it.statusStyled(), innerW))
		}
	}
	for len(lines) < n {
		lines = append(lines, strings.Repeat(" ", innerW))
	}

	style := stBorderOff
	if m.focus == focusQueue {
		style = stBorderOn
	}
	return style.Render(strings.Join(lines, "\n"))
}

// fitRow 把左右两段拼进固定宽度的一行, 超宽时截断左侧
func fitRow(left, right string, w int) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if lw+rw+2 > w {
		left = truncate.StringWithTail(left, uint(max(1, w-rw-2)), "…")
	}
	pad := w - lipgloss.Width(left) - rw
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// ---- 小工具 ----

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
