package main

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"
)

// apiToken 启动时从环境变量 SERVICE_MANAGER_TOKEN 读取。
// 为空表示不强制鉴权（仅 dev 模式），此时会打印警告。
var apiToken string

func init() {
	apiToken = os.Getenv("SERVICE_MANAGER_TOKEN")
	if apiToken == "" {
		log.Println("[WARN] SERVICE_MANAGER_TOKEN not set, authentication is DISABLED")
	}
}

// authMiddleware 校验 X-API-Token 头。
// 未配置 token 时放行；配置后所有非 OPTIONS 请求必须带正确 token。
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 放行 CORS 预检
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if apiToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("X-API-Token")
		// 使用常量时间比较防侧信道
		if subtle.ConstantTimeCompare([]byte(got), []byte(apiToken)) != 1 {
			respondError(w, http.StatusUnauthorized, "invalid or missing X-API-Token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
