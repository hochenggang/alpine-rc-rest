package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   connectTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   connectTimeout,
			ResponseHeaderTimeout: connectTimeout,
			ExpectContinueTimeout: connectTimeout,
			IdleConnTimeout:       90 * time.Second,
		},
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

// partialState 记录 performCreate 过程中已成功建立的资产，
// 出错时按反序清理；只清理本流程创建的，不动 user（与 DELETE 一致）。
type partialState struct {
	dirCreated  bool // os.MkdirAll 成功
	userEnsured bool // ensureProjectUser 成功（不清）
	binWritten  bool // downloadBinary 成功
	envWritten  bool // writeEnvFile 成功
	initWritten bool // writeServiceScript 成功
	chowned     bool // chownProjectDir 成功
	enabled     bool // rc-update add 成功
	started     bool // rc-service start 成功
}

func (p *partialState) rollback(name string) {
	// 反序：service → runlevel → init script → env → bin → dir
	if p.started {
		_ = stopService(name)
	}
	if p.enabled {
		_ = removeFromAllRunlevels(name)
	}
	if p.initWritten {
		_ = deleteServiceScript(name)
	}
	if p.envWritten {
		_ = os.Remove(projectEnvPath(name))
	}
	if p.binWritten {
		_ = os.Remove(projectBinPath(name))
	}
	if p.dirCreated {
		_ = os.RemoveAll(projectDir(name))
	}
	// 不删 user/group（与 DELETE 一致）
}

// performCreate 完整创建流程：准备目录 → 用户 → 下载二进制 → 写 env → 写 init → 授权 → 启动
// 任何中间步骤失败会回滚已建资产（不动 user）。
func performCreate(req CreateProjectRequest) error {
	name := req.ProjectName
	var st partialState
	defer func() {
		// 失败回滚：handler 收到 err 就 500
	}()
	rollback := func() { st.rollback(name) }

	if err := os.MkdirAll(projectDir(name), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", projectDir(name), err)
	}
	st.dirCreated = true

	if err := ensureProjectUser(name); err != nil {
		rollback()
		return err
	}
	st.userEnsured = true

	if err := downloadBinary(req.BinURL, projectBinPath(name)); err != nil {
		rollback()
		return err
	}
	st.binWritten = true

	if err := os.Chmod(projectBinPath(name), 0o755); err != nil {
		rollback()
		return fmt.Errorf("chmod bin: %w", err)
	}

	if err := writeEnvFile(projectEnvPath(name), req.Env); err != nil {
		rollback()
		return err
	}
	st.envWritten = true

	if err := writeServiceScript(name, renderInitScript(name)); err != nil {
		rollback()
		return err
	}
	st.initWritten = true

	if err := chownProjectDir(projectDir(name), name, name); err != nil {
		rollback()
		return err
	}
	st.chowned = true

	// 启动前先 stop 容忍已运行实例
	_ = stopService(name)
	if err := enableService(name); err != nil {
		// enable 失败不回滚（init script 已建），只 log
		log.Printf("enable %s: %v", name, err)
	} else {
		st.enabled = true
	}
	if err := startService(name); err != nil {
		rollback()
		return fmt.Errorf("start %s: %w", name, err)
	}
	st.started = true
	return nil
}

// performUpdate 部分更新：提供哪个字段就更新哪个。
// bin / env 写入前先备份旧文件到 .bak，失败时回写；最后 chown + stop → start。
func performUpdate(name string, req UpdateProjectRequest) error {
	if req.BinURL == "" && len(req.Env) == 0 {
		return errors.New("nothing to update: provide bin_url and/or env")
	}
	if err := ensureProjectUser(name); err != nil {
		return err
	}

	type backup struct {
		binData []byte
		binMode os.FileMode
		binExisted bool
		envData string
		envExisted bool
	}
	var bkp backup
	hadBackup := false

	// 准备 backup（如果两边都改也无所谓；只对即将被改的字段备份）
	if req.BinURL != "" {
		if data, err := os.ReadFile(projectBinPath(name)); err == nil {
			info, _ := os.Stat(projectBinPath(name))
			bkp.binData = data
			if info != nil {
				bkp.binMode = info.Mode().Perm()
			}
			bkp.binExisted = true
		}
	}
	if req.Env != nil {
		if data, err := os.ReadFile(projectEnvPath(name)); err == nil {
			bkp.envData = string(data)
			bkp.envExisted = true
		}
	}
	hadBackup = bkp.binExisted || bkp.envExisted

	rollback := func() {
		if !hadBackup {
			return
		}
		// 回写顺序：先 env 再 bin（与原 performUpdate 顺序一致）
		if req.Env != nil && bkp.envExisted {
			_ = os.WriteFile(projectEnvPath(name), []byte(bkp.envData), 0o640)
		}
		if req.BinURL != "" && bkp.binExisted {
			_ = os.WriteFile(projectBinPath(name), bkp.binData, bkp.binMode)
		}
	}

	if req.BinURL != "" {
		if err := downloadBinary(req.BinURL, projectBinPath(name)); err != nil {
			rollback()
			return err
		}
		if err := os.Chmod(projectBinPath(name), 0o755); err != nil {
			rollback()
			return fmt.Errorf("chmod bin: %w", err)
		}
	}
	if req.Env != nil {
		if err := writeEnvFile(projectEnvPath(name), req.Env); err != nil {
			rollback()
			return err
		}
	}
	if err := chownProjectDir(projectDir(name), name, name); err != nil {
		rollback()
		return err
	}
	_ = stopService(name)
	if err := startService(name); err != nil {
		// 启动失败不回滚（bin/env/chown 已成功，调用方想回滚要重新 PUT）
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

// readProjectEnv 解析 K=V 文件，支持：
//   KEY=value
//   KEY='value with spaces'
//   KEY='it''s'      # 标准 shell escape：'\'' 表示字面 '
func readProjectEnv(name string) (map[string]string, error) {
	return readEnvFile(projectEnvPath(name))
}

// readEnvFile 从任意路径解析 K=V 文件（同 readProjectEnv 的解析规则）。
// 抽出独立函数便于测试。
func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
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
		k := line[:idx]
		v := strings.TrimSpace(line[idx+1:])
		// 反向：去外层单引号并把 '\'' 还原成 '
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
		}
		env[k] = v
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
// 只列本服务管理的项目：/etc/init.d/<name> 存在 且 /opt/<name> 目录存在。
// 这样 crond / sshd 等系统服务不会被错误地列出来。
func listProjects(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir("/etc/init.d")
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]Project, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || name == "alpine-rc-rest" {
			continue
		}
		// 只列 /opt/<name> 存在的项目
		if _, err := os.Stat(projectDir(name)); err != nil {
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
// 停用并删除。任一步骤失败返回 500 并在日志中记录已成功的步骤。
// 响应：204 No Content（全部成功） / 500（部分失败）。
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
	// 收集错误，任一失败立即返 500（部分状态已改，log 出来）
	if err := stopService(name); err != nil {
		log.Printf("delete %s: stop: %v", name, err)
		respondError(w, http.StatusInternalServerError, "stop failed: "+err.Error())
		return
	}
	if err := removeFromAllRunlevels(name); err != nil {
		log.Printf("delete %s: rc-update del: %v", name, err)
		respondError(w, http.StatusInternalServerError, "rc-update del failed: "+err.Error())
		return
	}
	if err := deleteServiceScript(name); err != nil {
		log.Printf("delete %s: rm init script: %v", name, err)
		respondError(w, http.StatusInternalServerError, "remove init script failed: "+err.Error())
		return
	}
	if err := removeDir(filepath.Join("/opt", name)); err != nil {
		log.Printf("delete %s: rm /opt/%s: %v", name, name, err)
		respondError(w, http.StatusInternalServerError, "remove /opt/"+name+" failed: "+err.Error())
		return
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
