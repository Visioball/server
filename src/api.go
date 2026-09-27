package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type game struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	ImageURL   *string         `json:"imageUrl"`
	CreatedAt  string          `json:"createdAt"`
	Definition json.RawMessage `json:"definition"`
}

func uuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func problem(w http.ResponseWriter, status int, detail, field string) {
	body := map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": detail}
	if status == 400 || status == 413 || status == 415 || status == 422 {
		body["errors"] = []any{map[string]string{"field": field, "message": detail}}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == "/games":
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			problem(w, 405, "Use POST to create a game.", "")
			return
		}
		s.create(w, r)
	case strings.HasPrefix(r.URL.Path, "/games/"):
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			problem(w, 405, "Use GET to retrieve a game.", "")
			return
		}
		s.get(w, r)
	case r.URL.Path == "/audio" || r.URL.Path == "/image":
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			problem(w, 405, "Use POST to upload a file.", "")
			return
		}
		s.upload(w, r)
	default:
		problem(w, 404, "Unknown endpoint.", "")
	}
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			problem(w, 413, "Request body exceeds the size limit.", "")
		} else {
			problem(w, 400, "Unable to read request body.", "")
		}
		return nil, false
	}
	return b, true
}

func validURL(raw string, local bool) bool {
	if utf8.RuneCountInString(raw) > 2048 || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return false
	}
	return u.Scheme == "https" || (local && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}

// Walk tokens so duplicate keys cannot silently overwrite earlier values.
func jsonDepth(d *json.Decoder, depth int) (int, error) {
	t, err := d.Token()
	if err != nil {
		return depth, err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return depth, nil
	}
	depth++
	max := depth
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			k, e := d.Token()
			if e != nil {
				return max, e
			}
			key, ok := k.(string)
			if !ok || keys[key] {
				return max, fmt.Errorf("duplicate or invalid key")
			}
			keys[key] = true
		}
		n, e := jsonDepth(d, depth)
		if e != nil {
			return max, e
		}
		if n > max {
			max = n
		}
	}
	_, err = d.Token()
	return max, err
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) || r.Header.Get("Content-Encoding") != "" {
		problem(w, 415, "Use application/json with UTF-8 and no content encoding.", "")
		return
	}
	b, ok := readBody(w, r, 65536)
	if !ok {
		return
	}
	if !utf8.Valid(b) || !json.Valid(b) {
		problem(w, 400, "Body must be valid UTF-8 JSON.", "")
		return
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	depth, err := jsonDepth(d, 0)
	if err != nil {
		problem(w, 400, "Duplicate object keys are not allowed.", "")
		return
	}
	if depth > 20 {
		problem(w, 422, "JSON nesting must not exceed 20 levels.", "")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		problem(w, 422, "Body must be an object.", "")
		return
	}
	for k := range fields {
		if k != "title" && k != "imageUrl" && k != "definition" {
			problem(w, 422, "Unknown field.", "/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"))
			return
		}
	}
	var g game
	if json.Unmarshal(fields["title"], &g.Title) != nil || strings.TrimSpace(g.Title) == "" || utf8.RuneCountInString(g.Title) > 120 {
		problem(w, 422, "Title must contain 1–120 characters and non-whitespace text.", "/title")
		return
	}
	if v, exists := fields["imageUrl"]; exists && string(v) != "null" {
		var u string
		if json.Unmarshal(v, &u) != nil || !validURL(u, s.localHTTP) {
			problem(w, 422, "Image URL must be an absolute HTTPS URL without credentials or whitespace.", "/imageUrl")
			return
		}
		g.ImageURL = &u
	}
	def := bytes.TrimSpace(fields["definition"])
	if len(def) == 0 || def[0] != '{' {
		problem(w, 422, "Definition must be an object.", "/definition")
		return
	}
	g.Definition = def
	g.ID = uuid()
	g.CreatedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	body, _ := json.Marshal(g)
	if _, err = s.db.ExecContext(r.Context(), "INSERT INTO games (id, body) VALUES ($1, $2)", g.ID, string(body)); err != nil {
		problem(w, 503, "Game storage is unavailable.", "")
		return
	}
	w.Header().Set("Location", "/games/"+g.ID)
	reply(w, 201, g)
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/games/")
	if !idPattern.MatchString(id) {
		problem(w, 400, "ID must be a canonical UUIDv4.", "")
		return
	}
	var body json.RawMessage
	err := s.db.QueryRowContext(r.Context(), "SELECT body FROM games WHERE id=$1", strings.ToLower(id)).Scan(&body)
	if err == sql.ErrNoRows {
		problem(w, 404, "No game exists with this ID.", "")
		return
	}
	if err != nil {
		problem(w, 503, "Game storage is unavailable.", "")
		return
	}
	reply(w, 200, body)
}
