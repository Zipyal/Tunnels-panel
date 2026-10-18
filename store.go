package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Tunnel это то, что ввёл пользователь
type Tunnel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Command  string `json:"command"`
	Password string `json:"password"`
	URL      string `json:"url"`
}

// Settings это данные пользователя, общие для всех туннелей:
// {login} в командах заменяется на Login, а Password берётся там, где у туннеля свой пароль не задан
type Settings struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// StateEntry запоминает процесс ssh, чтобы после перезапуска панели мы знали, что останавливать
type StateEntry struct {
	Pid     int   `json:"pid"`
	Started int64 `json:"started"`
}

// Store хранит данные в json-файлах: tunnels.json и state.json.
// Для десятка записей база данных не нужна, а файл можно открыть и поправить руками.
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) LogPath(id string) string { return filepath.Join(s.dir, "logs", id+".log") }

func (s *Store) Tunnels() []Tunnel {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Tunnel
	s.load("tunnels.json", &list)
	return list
}

func (s *Store) UpdateTunnels(change func([]Tunnel) []Tunnel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Tunnel
	s.load("tunnels.json", &list)
	s.save("tunnels.json", change(list))
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v Settings
	s.load("settings.json", &v)
	return v
}

func (s *Store) SaveSettings(v Settings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.save("settings.json", v)
}

func (s *Store) State() map[string]StateEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state()
}

func (s *Store) UpdateState(change func(map[string]StateEntry)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state()
	change(st)
	s.save("state.json", st)
}

func (s *Store) state() map[string]StateEntry {
	st := map[string]StateEntry{}
	s.load("state.json", &st)
	return st
}

func (s *Store) load(name string, v any) {
	if raw, err := os.ReadFile(filepath.Join(s.dir, name)); err == nil {
		_ = json.Unmarshal(raw, v)
	}
}

// Пишем во временный файл и переименовываем: файл не окажется записанным наполовину
func (s *Store) save(name string, v any) {
	raw, _ := json.MarshalIndent(v, "", "  ")
	path := filepath.Join(s.dir, name)
	if os.WriteFile(path+".tmp", raw, 0o600) == nil { // в файле пароли, права только владельцу
		_ = os.Rename(path+".tmp", path)
	}
}
