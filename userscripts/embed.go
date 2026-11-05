// Package userscripts 内嵌管理端可一键安装的用户脚本
package userscripts

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed *.user.js
var files embed.FS

// Handler 返回用户脚本内容，管理端「安装油猴脚本」入口直接打开该地址
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name, err := userScriptName()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data, err := files.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}

// userScriptName 返回内嵌的唯一用户脚本文件名
func userScriptName() (string, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".user.js") {
			return entry.Name(), nil
		}
	}
	return "", fs.ErrNotExist
}
