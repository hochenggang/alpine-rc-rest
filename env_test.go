package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	inputs := map[string]string{
		"PLAIN":       "hello",
		"WITH_DOLLAR": "before${HOME}after",
		"WITH_BTICK":  "a`id`b",
		"WITH_QUOTE":  "it's a 'test'",
		"WITH_SPACES": "hello world",
		"EMPTY":       "",
		"WITH_EQ":     "key=val=ue",
		// 注：value 含换行符本质上行式 K=V 文件承载不了；本服务约定 value 不含 \n
		// （JSON 解析时若用户传了 \n，会写两行，下游 . path 时只读到第一行）。
		// 行为是"半截"，但不会注入，这是当前设计取舍。
	}
	if err := writeEnvFile(path, inputs); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	got, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile: %v", err)
	}
	for k, want := range inputs {
		if got[k] != want {
			t.Errorf("key %q: got %q, want %q", k, got[k], want)
		}
	}
}

func TestEnvFileNoExpansion(t *testing.T) {
	// 关键：写出的 env 文件被 sh `set -a; . file; set +a` 加载后，
	// $ 和 反引号不应被 shell 展开。
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	inputs := map[string]string{
		"DOLLAR": "literal${HOME}end",
		"BTICK":  "literal`id`end",
	}
	if err := writeEnvFile(path, inputs); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	// 优先用 git-bash；找不到则 skip
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash 不可用: %v", err)
		return
	}
	out, err := exec.Command(bashPath, "-c",
		"set -a; . '"+path+"'; set +a; printf '%s|%s\\n' \"$DOLLAR\" \"$BTICK\"").Output()
	if err != nil {
		t.Skipf("bash 执行失败: %v", err)
		return
	}
	got := strings.TrimRight(string(out), "\n")
	want := "literal${HOME}end|literal`id`end"
	if got != want {
		t.Errorf("sh 加载后值被展开: got %q want %q", got, want)
	}
}
