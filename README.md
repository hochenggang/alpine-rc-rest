# alpine-rc-rest

> 极简的 Alpine OpenRC 进程管理 REST API。**零第三方依赖**，单文件二进制。

[![Release](https://img.shields.io/github/v/release/hochenggang/alpine-rc-rest)](https://github.com/hochenggang/alpine-rc-rest/releases)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

## 它做什么

提供一个 HTTP 接口，让你通过 **JSON** 创建 / 更新 / 删除一个被 OpenRC 守护的进程。

每个"项目" = 一个 nologin 系统用户 + 一个二进制文件 + 一组环境变量 + 一个 OpenRC 服务。

## 特性

- 🚀 **零依赖**：仅用 Go 标准库，单文件二进制 ~6MB
- 🔗 **HTTP 拉取二进制**：传一个 `bin_url` 就完事
- 🔐 **用户隔离**：每个项目自动创建同名 **nologin 系统用户 + 组**，目录权限 `0750`
- 🔒 **Token 鉴权**：`X-API-Token` + `crypto/subtle.ConstantTimeCompare`
- 🔁 **部分更新**：PUT 一次可以只换二进制 / 只改 env / 两者都改
- 🌐 **CORS**：开箱即用

## 一键安装

```sh
curl -fsSL https://raw.githubusercontent.com/hochenggang/alpine-rc-rest/main/install.sh | sudo sh

# 指定版本
VERSION=v1.0.0 curl -fsSL https://raw.githubusercontent.com/hochenggang/alpine-rc-rest/main/install.sh | sudo sh

# 本地开发
sudo sh install.sh ./dist/alpine-rc-rest-linux-amd64
```

安装器自动：探测平台 → 下载二进制 → 写入 `/etc/init.d/alpine-rc-rest` 与 `/etc/conf.d/alpine-rc-rest` → 打印下一步。

## 快速开始

```sh
# 1. 设置 Token（强烈推荐）
sudo sed -i 's|^SERVICE_MANAGER_TOKEN=.*|SERVICE_MANAGER_TOKEN="$(openssl rand -hex 24)"|' /etc/conf.d/alpine-rc-rest

# 2. 启动
sudo rc-service alpine-rc-rest start
sudo rc-update add alpine-rc-rest default

# 3. 创建一个项目
TOKEN=$(sudo grep SERVICE_MANAGER_TOKEN /etc/conf.d/alpine-rc-rest | cut -d'"' -f2)
curl -X POST http://127.0.0.1:8080/api/v1/project \
  -H "X-API-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "project_name": "myapi",
    "bin_url":      "https://github.com/me/myapi/releases/latest/download/myapi-linux-amd64",
    "env": {
      "PORT":        "8080",
      "DB_URL":      "postgres://localhost/mydb",
      "LOG_LEVEL":   "info"
    }
  }'
```

## 配置文件布局

```
/opt/<project_name>/
├── bin              # 主二进制（chmod 0755，归属 <project_name>:<project_name>）
└── env              # 环境变量文件（K=V 每行）

/etc/init.d/<project_name>   # OpenRC 脚本（自动生成，0755）
/run/<project_name>.pid
```

`/etc/init.d/<project_name>` 由本服务自动生成，结构极简：

```sh
#!/sbin/openrc-run

name='myapi'
description='managed by alpine-rc-rest'

command="/opt/myapi/bin"
command_user='myapi'
command_background=true
pidfile='/run/myapi.pid'

start_pre() {
    if [ -f /opt/myapi/env ]; then
        set -a
        . /opt/myapi/env
        set +a
    fi
}
```

## API

BaseURL = `/api/v1/project`

### 路由总表

| 方法 | 路径 | 功能 |
|------|------|------|
| `GET` | `/api/v1/project` | 列表 |
| `GET` | `/api/v1/project/{name}` | 详情 |
| `POST` | `/api/v1/project` | 创建 |
| `PUT` | `/api/v1/project/{name}` | 部分更新（`bin_url?`、`env?`） |
| `DELETE` | `/api/v1/project/{name}` | 删除 |
| `POST` | `/api/v1/project/{name}/start` | 启动 |
| `POST` | `/api/v1/project/{name}/stop` | 停止 |
| `POST` | `/api/v1/project/{name}/restart` | 重启 |

### 统一响应

```json
{ "code": 200, "message": "success", "data": { ... } }
```

错误：

```json
{ "code": 404, "message": "Not Found", "error": "project not found" }
```

### 鉴权

通过 `SERVICE_MANAGER_TOKEN` 环境变量启用：

| 状态 | 行为 |
|------|------|
| 未设置 | `[WARN] authentication is DISABLED`，所有请求放行 |
| 已设置 | 所有非 `OPTIONS` 请求必须带正确 `X-API-Token`，否则 401 |

```sh
curl -H "X-API-Token: $TOKEN" http://127.0.0.1:8080/api/v1/project
```

### 错误码

| HTTP | 触发条件 |
|------|----------|
| 200  | 成功 |
| 201  | 创建成功 |
| 204  | 删除成功 |
| 400  | JSON 解析失败、字段缺失、name/bin_url 非法、update 时未提供任何字段 |
| 401  | token 缺失或不匹配 |
| 404  | 项目不存在 |
| 409  | 创建时项目已存在 |
| 500  | 下载失败、文件 I/O、`rc-*` 失败、用户/组创建失败 |

### 资源限制

| 项 | 限制 |
|----|------|
| JSON 请求体 | 1 MB |
| `bin_url` 协议 | 仅 `http://` `https://` |
| `bin_url` 超时 | 2 min |
| `bin_url` 大小 | 50 MB |
| `project_name` | 正则 `^[a-zA-Z0-9_-]+$` |
| `env` key | 正则 `^[A-Za-z_][A-Za-z0-9_]*$` |

### `GET /api/v1/project`

列出 `/etc/init.d/` 下所有 init 脚本对应的项目（自动排除 `alpine-rc-rest` 自身）。

```json
{
  "code": 200,
  "message": "success",
  "data": [
    { "name": "myapi", "status": "started", "enabled": true, "env": { "PORT": "8080" } }
  ]
}
```

字段：
- `status`：`started` / `stopped` / `unknown`
- `enabled`：是否在 `default` 运行级别
- `env`：从 `/opt/<name>/env` 解析的 K=V

### `GET /api/v1/project/{name}`

获取单个项目详情。

```json
{
  "code": 200,
  "message": "success",
  "data": {
    "name": "myapi",
    "status": "started",
    "enabled": true,
    "env": { "PORT": "8080", "DB_URL": "postgres://..." }
  }
}
```

错误：400（非法 name）/ 404（不存在）

### `POST /api/v1/project` ⭐

创建项目。

请求体：

```json
{
  "project_name": "myapi",
  "bin_url":      "https://github.com/me/myapi/releases/latest/download/myapi-linux-amd64",
  "env": {
    "PORT":      "8080",
    "DB_URL":    "postgres://localhost/mydb",
    "LOG_LEVEL": "info"
  }
}
```

| 字段 | 必填 | 说明 |
|------|------|------|
| `project_name` | 是 | 项目名（也是系统用户名 / 组名） |
| `bin_url` | 是 | 仅 http/https；≤50MB；≤2min |
| `env` | 否 | K=V 映射，key 须匹配 `^[A-Za-z_][A-Za-z0-9_]*$` |

服务器行为：

```
1. 校验 name 正则
2. projectExists → 409
3. mkdir -p /opt/<name>
4. ensureProjectUser(<name>)  // 幂等
5. downloadBinary(bin_url, /opt/<name>/bin)  // 临时文件 → atomic rename
6. chmod 0755 /opt/<name>/bin
7. writeEnvFile(/opt/<name>/env, env)
8. writeInitScript(<name>)  → /etc/init.d/<name>
9. chownProjectDir(/opt/<name>, <name>, <name>)  // 0750
10. rc-update add <name> default
11. rc-service <name> start
12. 201 Created
```

```sh
curl -X POST http://127.0.0.1:8080/api/v1/project \
  -H "X-API-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "project_name": "myapi",
    "bin_url":      "https://example.com/myapi",
    "env": { "PORT": "8080" }
  }'
```

### `PUT /api/v1/project/{name}`

**部分更新**：提供哪个字段就更新哪个。更新后自动 stop → start。

请求体（所有字段可选，但**至少一个**）：

```json
{
  "bin_url": "https://example.com/myapi-v2",
  "env":     { "PORT": "9090", "LOG_LEVEL": "debug" }
}
```

```sh
# 只改 env
curl -X PUT http://127.0.0.1:8080/api/v1/project/myapi \
  -H "X-API-Token: $TOKEN" -H "Content-Type: application/json" \
  -d '{ "env": { "PORT": "9090" } }'

# 只换二进制
curl -X PUT http://127.0.0.1:8080/api/v1/project/myapi \
  -H "X-API-Token: $TOKEN" -H "Content-Type: application/json" \
  -d '{ "bin_url": "https://example.com/myapi-v2" }'
```

### `DELETE /api/v1/project/{name}`

停用并删除。

1. `rc-service <name> stop`（忽略错误）
2. `rc-update del <name>`（移除所有运行级别）
3. `os.Remove(/etc/init.d/<name>)`
4. `os.RemoveAll(/opt/<name>)`（清理部署目录）

**注意**：**不**删除 nologin 系统用户/组（运维手动 `deluser` / `delgroup`）。

响应 204 No Content（无 Body）。

### `POST /api/v1/project/{name}/start` / `stop` / `restart`

```sh
curl -X POST -H "X-API-Token: $TOKEN" \
  http://127.0.0.1:8080/api/v1/project/myapi/start
```

```sh
curl -X POST -H "X-API-Token: $TOKEN" \
  http://127.0.0.1:8080/api/v1/project/myapi/stop
```

```sh
curl -X POST -H "X-API-Token: $TOKEN" \
  http://127.0.0.1:8080/api/v1/project/myapi/restart
```

`restart` 在 stopped 状态下也会启动（OpenRC 行为）。

### env 文件格式

```sh
# comment
KEY=value
KEY2=value with spaces
EMPTY=
```

写入前 key 校验：`^[A-Za-z_][A-Za-z0-9_]*$`。

### Init 脚本模板

`/etc/init.d/<name>` 由本服务自动生成：

```sh
#!/sbin/openrc-run

name='myapi'
description='managed by alpine-rc-rest'

command="/opt/myapi/bin"
command_user='myapi'
command_background=true
pidfile='/run/myapi.pid'

start_pre() {
    if [ -f /opt/myapi/env ]; then
        set -a
        . /opt/myapi/env
        set +a
    fi
}
```

`start_pre` 由 root 执行，`set -a; . /opt/myapi/env; set +a` 让所有 K=V 自动 export。OpenRC 切到 `command_user` 启动子进程，子进程继承这些变量。

### CORS

```
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS
Access-Control-Allow-Headers: Content-Type, X-API-Token
Access-Control-Expose-Headers: X-API-Token
```

### 完整 deploy 脚本

```sh
#!/usr/bin/env sh
set -eu
TOKEN="${SERVICE_MANAGER_TOKEN:-}"
HOST="${HOST:-http://127.0.0.1:8080}"
NAME="${1:?usage: deploy.sh <name> <bin_url> [env_json]}"
URL="${2:?usage: deploy.sh <name> <bin_url> [env_json]}"
ENV_JSON="${3:-{\}}"

curl -fsS -X POST "$HOST/api/v1/project" \
  -H "X-API-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d "$(printf '{"project_name":"%s","bin_url":"%s","env":%s}' "$NAME" "$URL" "$ENV_JSON")"
```

## 配置

通过环境变量：

| 变量 | 默认 | 说明 |
|------|------|------|
| `SERVICE_MANAGER_TOKEN` | （空） | 启用鉴权。未设置时所有请求放行（dev 模式） |
| `LISTEN_ADDR` | `:8080` | 监听地址（编译时硬编码于 `main.go`） |

## 安全

| 项 | 限制 |
|---|---|
| bin_url 协议 | 仅 `http://` `https://` |
| bin_url 超时 | 2 min |
| bin_url 大小 | 50 MB |
| 用户隔离 | 每服务独立 nologin 用户，目录权限 `0750` |
| 鉴权 | `subtle.ConstantTimeCompare` 防侧信道 |

⚠️ **必须以 root 身份运行**（操作 `/etc/init.d` 与 `rc-*` 命令）。务必设置 `SERVICE_MANAGER_TOKEN`，并通过防火墙 / 仅监听 127.0.0.1 做纵深防御。

## 源码编译

需求：Go 1.22+

```sh
go build -o alpine-rc-rest .
sudo ./alpine-rc-rest
```

## 开发

```sh
go vet ./...
# 交叉编译
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o alpine-rc-rest-linux-arm64 .
```

### 目录结构

```
alpine-rc-rest/
├── main.go        # 入口、路由、cors+auth、优雅关闭
├── auth.go        # X-API-Token 中间件
├── service.go     # rc-* 封装、文件 I/O、用户/组、chown
├── project.go     # 项目生命周期 + downloadBinary + renderInitScript
├── utils.go       # Response 统一格式 + name 正则
├── install.sh
├── design.md
├── api.md
└── .github/workflows/release.yml
```

## 文档

- [README.md](file:///c:/Users/Administrator/Documents/codes/alpine-rc-rest/README.md)：本文件
- [design.md](file:///c:/Users/Administrator/Documents/codes/alpine-rc-rest/design.md)：架构与设计
- [api.md](file:///c:/Users/Administrator/Documents/codes/alpine-rc-rest/api.md)：完整 API 文档

## 路线图

- 进程日志查看
- 资源占用查看
- 多版本回滚

## License

MIT
