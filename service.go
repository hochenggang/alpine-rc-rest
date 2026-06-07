package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// runCommand 同步执行命令并返回合并输出
func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ---------- OpenRC 封装 ----------

// getServiceStatus 查询服务状态：started / stopped / unknown
func getServiceStatus(name string) string {
	out, err := runCommand("rc-service", name, "status")
	if err != nil {
		// rc-service 在 stopped 时返回非零
		if strings.Contains(out, "stopped") {
			return "stopped"
		}
		return "unknown"
	}
	return "started"
}

// isEnabledInDefault 查询是否在 default 运行级别启用
func isEnabledInDefault(name string) bool {
	out, err := runCommand("rc-update", "show")
	if err != nil {
		return false
	}
	// 输出形如：myserver | default | ...
	// 或：myserver |                       （未启用）
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "|")
		if len(fields) >= 2 && strings.TrimSpace(fields[0]) == name {
			for _, f := range fields[1:] {
				if strings.TrimSpace(f) == "default" {
					return true
				}
			}
		}
	}
	return false
}

func enableService(name string) error {
	_, err := runCommand("rc-update", "add", name, "default")
	return err
}

func startService(name string) error {
	_, err := runCommand("rc-service", name, "start")
	return err
}

func stopService(name string) error {
	_, err := runCommand("rc-service", name, "stop")
	return err
}

func restartService(name string) error {
	_, err := runCommand("rc-service", name, "restart")
	return err
}

func removeFromAllRunlevels(name string) error {
	_, err := runCommand("rc-update", "del", name)
	return err
}

// ---------- 文件系统 ----------

// serviceExists 通过 /etc/init.d/<name> 是否存在判断
func serviceExists(name string) bool {
	_, err := os.Stat(filepath.Join("/etc/init.d", name))
	return err == nil
}

// writeServiceScript 写入 init 脚本（0755）
func writeServiceScript(name, content string) error {
	path := filepath.Join("/etc/init.d", name)
	return os.WriteFile(path, []byte(content), 0o755)
}

// deleteServiceScript 删除 init 脚本
func deleteServiceScript(name string) error {
	return os.Remove(filepath.Join("/etc/init.d", name))
}

// projectDir /opt/<name>
func projectDir(name string) string {
	return "/opt/" + name
}

// projectBinPath /opt/<name>/bin
func projectBinPath(name string) string {
	return projectDir(name) + "/bin"
}

// projectEnvPath /opt/<name>/env
func projectEnvPath(name string) string {
	return projectDir(name) + "/env"
}

func removeDir(p string) error {
	return os.RemoveAll(p)
}

// ---------- 用户与权限 ----------

// ensureProjectUser 幂等创建同名 nologin 系统用户与组
func ensureProjectUser(name string) error {
	if _, err := runCommand("getent", "group", name); err != nil {
		if out, err := runCommand("addgroup", "-S", name); err != nil {
			return fmt.Errorf("addgroup %s: %s: %w", name, strings.TrimSpace(out), err)
		}
	}
	if _, err := runCommand("getent", "passwd", name); err != nil {
		out, err := runCommand("adduser", "-D", "-H", "-s", "/sbin/nologin", "-g", name, name)
		if err != nil {
			return fmt.Errorf("adduser %s: %s: %w", name, strings.TrimSpace(out), err)
		}
	}
	return nil
}

// chownProjectDir 递归 chown + chmod：目录 0750 / 文件 0640 / 可执行 0750，其他人无权限
func chownProjectDir(path, user, group string) error {
	if out, err := runCommand("chown", "-R", user+":"+group, path); err != nil {
		return fmt.Errorf("chown %s: %s: %w", path, strings.TrimSpace(out), err)
	}
	if out, err := runCommand("chmod", "-R", "u+rwX,g+rX,o-rwx", path); err != nil {
		return fmt.Errorf("chmod %s: %s: %w", path, strings.TrimSpace(out), err)
	}
	return nil
}

// ---------- env 文件 ----------

// writeEnvFile 把 env map 写入 /opt/<name>/env
// 跳过空 key、校验 K=V 格式
func writeEnvFile(path string, env map[string]string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for k, v := range env {
		if !validEnvKey(k) {
			return fmt.Errorf("invalid env key: %q", k)
		}
		if _, err := fmt.Fprintf(w, "%s=%s\n", k, v); err != nil {
			return err
		}
	}
	return w.Flush()
}

var envKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validEnvKey(k string) bool {
	return envKeyRegex.MatchString(k)
}
