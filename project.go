package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------- 限制 ----------

const (
	maxJSONBodyBytes  = 1 << 20  // 1 MB
	maxDownloadBytes  = 50 << 20 // 50 MB
	downloadTimeout   = 2 * time.Minute
	connectTimeout    = 10 * time.Second
	downloadUserAgent = "alpine-rc-rest/1.0"
)

// ---------- 资源 / DTO ----------

type Project struct {
	Name    string            `json:"name"`
	Status  string            `json:"status"`
	Enabled bool              `json:"enabled"`
	Env     map[string]string `json:"env,omitempty"`
}

type CreateProjectRequest struct {
	ProjectName string            `json:"project_name"`
	BinURL      string            `json:"bin_url"`
	Env         map[string]string `json:"env"`
}

type UpdateProjectRequest struct {
	BinURL string            `json:"bin_url,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// ---------- HTTP 下载 ----------

// downloadBinary 从 URL 拉取二进制到 dst（覆盖写）。
// 约束：仅 http/https；超时 2min；最大 50MB；零字节视为错误。
func downloadBinary(rawURL, dst string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme: %s (only http/https)", u.Scheme)
	}

	client := &http.Client{
		Timeout: downloadTimeout,
		// 默认 CheckRedirect 跟随最多 10 次
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", downloadUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download: http %d", resp.StatusCode)
	}

	// 限流：若服务器声明 Content-Length 超出限制则直接拒绝
	if resp.ContentLength > maxDownloadBytes {
		return fmt.Errorf("download: content-length %d exceeds limit %d", resp.ContentLength, maxDownloadBytes)
	}

	// 写入临时文件，下载完成后原子 rename
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	limited := io.LimitReader(resp.Body, maxDownloadBytes+1)
	n, err := io.Copy(out, limited)
	cerr := out.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("download: %w", err)
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("download close: %w", cerr)
	}
	if n == 0 {
		_ = os.Remove(tmp)
		return errors.New("download: empty body")
	}
	if n > maxDownloadBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("download: size %d exceeds limit %d", n, maxDownloadBytes)
	}
	return os.Rename(tmp, dst)
}

// ---------- init 脚本生成 ----------

// renderInitScript 生成极简的 OpenRC init 脚本：
// command = /opt/<name>/bin
// command_user = <name>（nologin）
// start_pre 中 source /opt/<name>/env
func renderInitScript(name string) string {
	return fmt.Sprintf(`#!/sbin/openrc-run

name='%s'
description='managed by alpine-rc-rest'

command=%q
command_user='%s'
command_background=true
pidfile='/run/%s.pid'

start_pre() {
    if [ -f %q ]; then
        set -a
        . %q
        set +a
    fi
}
`, name, projectBinPath(name), name, name, projectEnvPath(name), projectEnvPath(name))
}

// ---------- 业务逻辑 ----------

// performCreate 完整创建流程：准备目录 → 用户 → 下载二进制 → 写 env → 写 init → 授权 → 启动
func performCreate(req CreateProjectRequest) error {
	name := req.ProjectName
	if err := os.MkdirAll(projectDir(name), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", projectDir(name), err)
	}
	if err := ensureProjectUser(name); err != nil {
		return err
	}
	if err := downloadBinary(req.BinURL, projectBinPath(name)); err != nil {
		return err
	}
	if err := os.Chmod(projectBinPath(name), 0o755); err != nil {
		return fmt.Errorf("chmod bin: %w", err)
	}
	if err := writeEnvFile(projectEnvPath(name), req.Env); err != nil {
		return err
	}
	if err := writeServiceScript(name, renderInitScript(name)); err != nil {
		return err
	}
	if err := chownProjectDir(projectDir(name), name, name); err != nil {
		return err
	}
	_ = stopService(name)
	if err := enableService(name); err != nil {
		log.Printf("enable %s: %v", name, err)
	}
	if err := startService(name); err != nil {
		log.Printf("start %s: %v", name, err)
	}
	return nil
}

// performUpdate 部分更新：提供哪个字段就更新哪个
func performUpdate(name string, req UpdateProjectRequest) error {
	if req.BinURL == "" && len(req.Env) == 0 {
		return errors.New("nothing to update: provide bin_url and/or env")
	}
	if err := ensureProjectUser(name); err != nil {
		return err
	}
	if req.BinURL != "" {
		if err := downloadBinary(req.BinURL, projectBinPath(name)); err != nil {
			return err
		}
		if err := os.Chmod(projectBinPath(name), 0o755); err != nil {
			return fmt.Errorf("chmod bin: %w", err)
		}
	}
	if req.Env != nil {
		if err := writeEnvFile(projectEnvPath(name), req.Env); err != nil {
			return err
		}
	}
	if err := chownProjectDir(projectDir(name), name, name); err != nil {
		return err
	}
	_ = stopService(name)
	if err := startService(name); err != nil {
		log.Printf("restart %s: %v", name, err)
	}
	return nil
}

// lookupName 校验 name 合法且项目存在；不通过则写错误并返回 false
func lookupName(w http.ResponseWriter, name string) bool {
	if !isValidServiceName(name) {
		respondError(w, http.StatusBadRequest, "invalid project name")
		return false
	}
	if !serviceExists(name) {
		respondError(w, http.StatusNotFound, "project not found")
		return false
	}
	return true
}

// ---------- handlers ----------

func readProjectEnv(name string) (map[string]string, error) {
	data, err := os.ReadFile(projectEnvPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	env := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		env[line[:idx]] = line[idx+1:]
	}
	return env, nil
}

func projectInfo(name string) Project {
	env, _ := readProjectEnv(name)
	return Project{
		Name:    name,
		Status:  getServiceStatus(name),
		Enabled: isEnabledInDefault(name),
		Env:     env,
	}
}

// listProjects GET /api/v1/project
func listProjects(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir("/etc/init.d")
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]Project, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		// 过滤掉 alpine-rc-rest 自身
		if name == "alpine-rc-rest" {
			continue
		}
		out = append(out, projectInfo(name))
	}
	respondOK(w, out)
}

// getProject GET /api/v1/project/{name}
func getProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !lookupName(w, name) {
		return
	}
	respondOK(w, projectInfo(name))
}

// createProject POST /api/v1/project
func createProject(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	defer r.Body.Close()
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if !isValidServiceName(req.ProjectName) {
		respondError(w, http.StatusBadRequest, "invalid or missing project_name")
		return
	}
	if req.BinURL == "" {
		respondError(w, http.StatusBadRequest, "bin_url is required")
		return
	}
	if serviceExists(req.ProjectName) {
		respondError(w, http.StatusConflict, "project already exists")
		return
	}
	if err := performCreate(req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondCreated(w, projectInfo(req.ProjectName))
}

// updateProject PUT /api/v1/project/{name}
func updateProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidServiceName(name) {
		respondError(w, http.StatusBadRequest, "invalid project name")
		return
	}
	if !serviceExists(name) {
		respondError(w, http.StatusNotFound, "project not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	defer r.Body.Close()
	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := performUpdate(name, req); err != nil {
		if strings.HasPrefix(err.Error(), "nothing to update") {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondOK(w, projectInfo(name))
}

// deleteProject DELETE /api/v1/project/{name}
func deleteProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidServiceName(name) {
		respondError(w, http.StatusBadRequest, "invalid project name")
		return
	}
	if !serviceExists(name) {
		respondError(w, http.StatusNotFound, "project not found")
		return
	}
	_ = stopService(name)
	if err := removeFromAllRunlevels(name); err != nil {
		log.Printf("rc-update del %s: %v", name, err)
	}
	if err := deleteServiceScript(name); err != nil {
		log.Printf("delete init script %s: %v", name, err)
	}
	if err := removeDir(filepath.Join("/opt", name)); err != nil {
		log.Printf("remove /opt/%s: %v", name, err)
	}
	log.Printf("project deleted: %s", name)
	w.WriteHeader(http.StatusNoContent)
}

// startProject POST /api/v1/project/{name}/start
func startProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !lookupName(w, name) {
		return
	}
	if err := startService(name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondOK(w, projectInfo(name))
}

// stopProject POST /api/v1/project/{name}/stop
func stopProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !lookupName(w, name) {
		return
	}
	if err := stopService(name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondOK(w, projectInfo(name))
}

// restartProject POST /api/v1/project/{name}/restart
func restartProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !lookupName(w, name) {
		return
	}
	if err := restartService(name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondOK(w, projectInfo(name))
}
