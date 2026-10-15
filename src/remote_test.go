package tscp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestAuthMethods(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir()) // 让默认私钥路径指向空目录
	t.Setenv("HOME", t.TempDir())

	// 只有密码
	ms, err := authMethods("pw", "")
	if err != nil || len(ms) != 1 {
		t.Fatalf("密码认证: %v %v", ms, err)
	}

	// 无密码无证书 → 明确报错
	if _, err = authMethods("", ""); err == nil {
		t.Fatal("无凭据应报错")
	}

	// 生成一把可用私钥, 用 -i 指定
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, block); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(keyFile, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	ms, err = authMethods("", keyFile)
	if err != nil || len(ms) != 1 {
		t.Fatalf("私钥认证: %v %v", ms, err)
	}

	// 私钥 + 密码 → 两种方式, 证书在前
	ms, err = authMethods("pw", keyFile)
	if err != nil || len(ms) != 2 {
		t.Fatalf("私钥+密码: %v %v", ms, err)
	}

	// -i 指向不存在的文件且无密码 → 明确报错
	if _, err = authMethods("", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("指定私钥不存在应报错")
	}
}
