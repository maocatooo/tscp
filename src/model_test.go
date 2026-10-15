package tscp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractPathsQuoted(t *testing.T) {
	got := extractPaths(`"D:\my files\a b.txt"`)
	want := []string{`D:\my files\a b.txt`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("带引号路径: got %v want %v", got, want)
	}
}

func TestExtractPathsMulti(t *testing.T) {
	got := extractPaths("\"C:\\a 1.txt\" \"C:\\b.txt\"")
	want := []string{`C:\a 1.txt`, `C:\b.txt`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("一行多个带引号路径: got %v want %v", got, want)
	}

	got = extractPaths("\"C:\\x\\a.txt\"\r\nC:\\x\\b.txt\n")
	want = []string{`C:\x\a.txt`, `C:\x\b.txt`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("多行路径: got %v want %v", got, want)
	}

	got = extractPaths("  \r\n\n")
	if len(got) != 0 {
		t.Fatalf("空输入应无结果: %v", got)
	}
}

func TestBuildQueueMsg(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(sub, "b.log")
	for _, f := range []string{f1, f2} {
		if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 单个文件: 上传到 cwd/base
	q := buildQueueMsg([]string{f1}, "/root")
	if len(q.items) != 1 || q.items[0].remote != "/root/a.txt" {
		t.Fatalf("单文件: %+v", q.items)
	}

	// 目录: 按相对结构展开
	q = buildQueueMsg([]string{dir}, "/data")
	if len(q.items) != 2 {
		t.Fatalf("目录展开应 2 项: %+v", q.items)
	}
	remotes := map[string]bool{}
	for _, it := range q.items {
		remotes[it.remote] = true
	}
	// 目录展开保留目录自身名字(与 scp -r 语义一致)
	base := "/data/" + filepath.Base(dir)
	if !remotes[base+"/a.txt"] || !remotes[base+"/sub/b.log"] {
		t.Fatalf("目录展开的远端路径: %v", remotes)
	}

	// 不存在 / 远程路径: 跳过并提示
	q = buildQueueMsg([]string{`C:\no\such.file`, "/etc/passwd"}, "/root")
	if len(q.items) != 0 || q.notice == "" {
		t.Fatalf("应全部跳过: items=%v notice=%q", q.items, q.notice)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	t.Setenv("TSCP_CACHE_DIR", t.TempDir())

	c := LoadCache() // 应自动创建目录并返回空缓存
	if c.Last != "" || len(c.Dirs) != 0 {
		t.Fatalf("初始缓存应为空: %+v", c)
	}
	if _, err := os.Stat(cacheDir()); err != nil {
		t.Fatalf("缓存目录未创建: %v", err)
	}

	c.Last = "root@10.10.10.3:22"
	c.Dirs[CacheKey("root", "10.10.10.3", 22)] = "/data/www"
	saveCache(c)

	got := LoadCache()
	if got.Last != "root@10.10.10.3:22" || got.Dirs["root@10.10.10.3:22"] != "/data/www" {
		t.Fatalf("缓存读写不一致: %+v", got)
	}
}

func TestParseCachedTarget(t *testing.T) {
	for _, tc := range []struct {
		in         string
		user, host string
		port       int
	}{
		{"root@10.10.10.3:2222", "root", "10.10.10.3", 2222},
		{"admin@web1", "admin", "web1", 22},
		{"web1:33", "root", "web1", 33},
		{"root@h:bad", "root", "h:bad", 22}, // 非数字端口按 host 的一部分处理
	} {
		u, h, p, ok := ParseCachedTarget(tc.in)
		if !ok || u != tc.user || h != tc.host || p != tc.port {
			t.Fatalf("%q: got %s/%s/%d ok=%v", tc.in, u, h, p, ok)
		}
	}
	if _, _, _, ok := ParseCachedTarget(""); ok {
		t.Fatal("空目标不应 ok")
	}
}

func TestWinToWSLPath(t *testing.T) {
	cases := map[string]string{
		`C:\Users\k\a.txt`:     "/mnt/c/Users/k/a.txt",
		`d:\my files\b.tar.gz`: "/mnt/d/my files/b.tar.gz",
		"/mnt/c/already.txt":   "/mnt/c/already.txt",
		`relative\path`:        `relative\path`,
		`\\server\share\a`:     `\\server\share\a`,
	}
	for in, want := range cases {
		if got := winToWSLPath(in); got != want {
			t.Fatalf("winToWSLPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikePathTextUnix(t *testing.T) {
	if !looksLikePathText("/mnt/d/tools/a.sh") || !looksLikePathText(`"/home/u/my file.txt"`) {
		t.Fatal("unix 路径应被识别")
	}
	if looksLikePathText("hello world") {
		t.Fatal("普通文本不应识别为路径")
	}
}

func TestHumanBytes(t *testing.T) {
	if humanBytes(512) != "512B" || humanBytes(2048) != "2.0KB" {
		t.Fatal("humanBytes 数值错误")
	}
}

func TestPasteRecognition(t *testing.T) {
	if !looksLikePathText(`D:\x.txt`) || !looksLikePathText(`"D:\my files\a.txt"`) ||
		!looksLikePathText(`\\server\share\a`) || looksLikePathText("hello world") {
		t.Fatal("looksLikePathText 判断错误")
	}
	if !pathCommitted("D:") || !pathCommitted(`"C`) || !pathCommitted(`\a`) || pathCommitted("ab") {
		t.Fatal("pathCommitted 判断错误")
	}
	if got := sanitizePastedText("\x1b[200~D:\\x.txt\x1b[201~"); got != `D:\x.txt` {
		t.Fatalf("括号粘贴标记未清除: %q", got)
	}
}

func TestFilterExisting(t *testing.T) {
	f := filepath.Join(t.TempDir(), "exists.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := filterExisting([]string{f, `C:\no\such.txt`, `  "` + f + `"  `})
	if len(got) != 2 || got[0] != f || got[1] != f {
		t.Fatalf("filterExisting: %v", got)
	}
}

func TestFlushPasteBuf(t *testing.T) {
	// 逐字符到达的拖拽路径, 累积后一次性识别
	f := filepath.Join(t.TempDir(), "drop.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mm model
	mm.cwd = "/root"
	mm.pasteBuf = `"` + f + `"`
	m2, cmd := mm.flushPasteBuf()
	if cmd == nil {
		t.Fatal("应产生入队命令")
	}
	if q := cmd().(queueMsg); len(q.items) != 1 || q.items[0].local != f || q.items[0].remote != "/root/drop.txt" {
		t.Fatalf("flushPasteBuf 识别: %+v", q.items)
	}
	if m2.(model).pasteBuf != "" || m2.(model).pasteSuppress {
		t.Fatal("缓冲未清空")
	}

	// 普通字母不应入队
	mm = model{pasteBuf: "ab"}
	if _, cmd = mm.flushPasteBuf(); cmd != nil {
		t.Fatal("非路径文本不应入队")
	}
}
