package main

import (
	"encoding/json"
	"net/http"
	"regexp"
)

// 响应
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

// 名称校验
var validNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func isValidServiceName(name string) bool {
	return validNameRegex.MatchString(name)
}

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func respondOK(w http.ResponseWriter, data any) {
	respondJSON(w, http.StatusOK, Response{Code: 200, Message: "success", Data: data})
}

func respondCreated(w http.ResponseWriter, data any) {
	respondJSON(w, http.StatusCreated, Response{Code: 201, Message: "created", Data: data})
}

func respondError(w http.ResponseWriter, status int, msg string) {
	respondJSON(w, status, Response{Code: status, Message: http.StatusText(status), Error: msg})
}
