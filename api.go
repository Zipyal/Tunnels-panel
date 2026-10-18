package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// API обрабатывает запросы /api/<action>
type API struct {
	store *Store
	mgr   *Manager
	quit  func() // выход из программы (гасит туннели и завершает процесс)
}

// Входные данные запроса: разные действия берут разные поля
type input struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Name     string `json:"name"`
	Password string `json:"password"`
	URL      string `json:"url"`
	Text     string `json:"text"`
	Login    string `json:"login"`
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/api/")
	if action == "health" { // для проверки, что на порту именно наша панель
		reply(w, 200, map[string]any{"ok": true, "app": "ssh-tunnels"})
		return
	}
	// Кастомный заголовок нельзя отправить с чужого сайта без CORS-разрешения,
	// так что страницы из интернета управлять панелью не смогут
	if r.Header.Get("X-Tunnels") != "1" {
		reply(w, 400, map[string]any{"error": "bad request"})
		return
	}
	var in input
	_ = json.NewDecoder(r.Body).Decode(&in) // пустое тело для GET это нормально

	data, status := a.handle(action, in)
	reply(w, status, data)
}

func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

type obj = map[string]any

func (a *API) handle(action string, in input) (any, int) {
	switch action {
	case "list":
		installing, installErr, installDone := a.mgr.InstallStatus()
		return obj{
			"tunnels":     a.mgr.Views(a.store.Tunnels()),
			"sshMissing":  a.mgr.SSHMissing(),
			"sshPath":     a.mgr.SSHPath(),
			"sshHint":     sshHint,
			"installing":  installing,
			"installErr":  installErr,
			"installDone": installDone,
		}, 200
	case "installssh":
		if e := a.mgr.InstallSSH(); e != "" {
			return obj{"ok": false, "error": e}, 200
		}
		return obj{"ok": true}, 200
	case "parse":
		c := ParseSSH(in.Command)
		if c.Err != "" {
			return obj{"error": c.Err}, 200
		}
		port := c.Port
		if port == "" {
			port = "22"
		}
		return obj{"host": c.Host, "user": c.User, "sshPort": port, "localPort": c.LocalPort(), "target": c.Target}, 200
	case "settings":
		return a.store.Settings(), 200
	case "savesettings":
		a.store.SaveSettings(Settings{Login: strings.TrimSpace(in.Login), Password: in.Password})
		return obj{"ok": true}, 200
	case "quit":
		go func() { // отвечаем браузеру и только потом выходим
			time.Sleep(300 * time.Millisecond)
			a.quit()
		}()
		return obj{"ok": true}, 200
	case "get":
		return a.find(in.ID)
	case "password":
		t, status := a.find(in.ID)
		if status != 200 {
			return t, status
		}
		tunnel := t.(Tunnel)
		_, pw := resolve(tunnel, a.store.Settings())
		return obj{"password": pw}, 200
	case "save":
		return a.save(in)
	case "delete", "down", "up":
		return a.single(action, in.ID)
	case "upall":
		var errs []string
		for _, t := range a.store.Tunnels() {
			if e := a.mgr.Start(t); e != "" {
				errs = append(errs, t.Name+": "+e)
			}
		}
		return obj{"ok": len(errs) == 0, "error": strings.Join(errs, "\n")}, 200
	case "downall":
		for _, t := range a.store.Tunnels() {
			a.mgr.Stop(t)
		}
		return obj{"ok": true}, 200
	case "import":
		return a.importText(in.Text), 200
	}
	return obj{"error": "unknown action"}, 404
}

// find возвращает Tunnel или ошибку 404
func (a *API) find(id string) (any, int) {
	for _, t := range a.store.Tunnels() {
		if t.ID == id {
			return t, 200
		}
	}
	return obj{"error": "Туннель не найден"}, 404
}

// single: up / down / delete для одного туннеля
func (a *API) single(action, id string) (any, int) {
	found, status := a.find(id)
	if status != 200 {
		return found, status
	}
	t := found.(Tunnel)

	switch action {
	case "up":
		if e := a.mgr.Start(t); e != "" {
			return obj{"ok": false, "error": e}, 200
		}
	case "down":
		if e := a.mgr.Stop(t); e != "" {
			return obj{"ok": false, "error": e}, 200
		}
	case "delete":
		a.mgr.Stop(t) // сначала гасим процесс, потом удаляем запись
		a.store.UpdateTunnels(func(list []Tunnel) []Tunnel {
			out := list[:0:0]
			for _, x := range list {
				if x.ID != id {
					out = append(out, x)
				}
			}
			return out
		})
	}
	return obj{"ok": true}, 200
}

func (a *API) save(in input) (any, int) {
	command := strings.TrimSpace(in.Command)
	c := ParseSSH(command)
	if c.Err != "" {
		return obj{"error": c.Err}, 422
	}
	rec := Tunnel{
		Name:     strings.TrimSpace(in.Name),
		Command:  command,
		Password: in.Password,
		URL:      normalizeURL(in.URL),
	}
	if rec.Name == "" {
		rec.Name = defaultName(c)
	}

	a.store.UpdateTunnels(func(list []Tunnel) []Tunnel {
		for i := range list {
			if in.ID != "" && list[i].ID == in.ID {
				rec.ID = in.ID
				list[i] = rec
				return list
			}
		}
		rec.ID = newID()
		return append(list, rec)
	})
	return obj{"ok": true}, 200
}

// Импорт из блокнота, по строке на туннель:
//
//	команда
//	название | команда | пароль | ссылка
func (a *API) importText(text string) obj {
	added := 0
	bad := []string{}

	a.store.UpdateTunnels(func(list []Tunnel) []Tunnel {
		for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var name, command, password, url string
			if f := splitTrim(line, 4); len(f) >= 2 {
				name, command, password, url = f[0], f[1], f[2], f[3]
			} else {
				command = f[0]
			}
			c := ParseSSH(command)
			if c.Err != "" {
				bad = append(bad, fmt.Sprintf("%s - %s", line, c.Err))
				continue
			}
			if name == "" {
				name = defaultName(c)
			}
			list = append(list, Tunnel{ID: newID(), Name: name, Command: command, Password: password, URL: normalizeURL(url)})
			added++
		}
		return list
	})
	return obj{"added": added, "bad": bad}
}

// splitTrim делит по "|" и всегда возвращает ровно n полей, если полей больше одного
func splitTrim(line string, n int) []string {
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) == 1 {
		return parts
	}
	for len(parts) < n {
		parts = append(parts, "")
	}
	return parts[:n]
}

func defaultName(c *SSHCommand) string {
	name := c.Host + fmt.Sprintf(":%d", c.LocalPort())
	if c.User != "" {
		name = c.User + "@" + name
	}
	return name
}

var schemeRe = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

// "localhost:1111" -> "http://localhost:1111", иначе браузер откроет это как относительную ссылку
func normalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if u != "" && !schemeRe.MatchString(u) {
		u = "http://" + u
	}
	return u
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
