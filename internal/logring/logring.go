// Package logring keeps the most recent log records in memory so
// administrators (and diagnostics API keys) can read them without shell
// access. Values of attributes that look like secrets are redacted.
package logring

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Record is one kept log line.
type Record struct {
	Time  time.Time         `json:"time"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

const size = 5000

var (
	mu   sync.Mutex
	ring = make([]Record, 0, size)
	next int
)

func add(r Record) {
	mu.Lock()
	defer mu.Unlock()
	if len(ring) < size {
		ring = append(ring, r)
		return
	}
	ring[next] = r
	next = (next + 1) % size
}

// Lines returns up to n recent records (oldest first) at or above level
// whose message or attributes contain q (case-insensitive).
func Lines(n int, level slog.Level, q string) []Record {
	mu.Lock()
	all := make([]Record, 0, len(ring))
	all = append(all, ring[next:]...)
	all = append(all, ring[:next]...)
	mu.Unlock()
	q = strings.ToLower(q)
	out := []Record{}
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		r := all[i]
		var lv slog.Level
		_ = lv.UnmarshalText([]byte(r.Level))
		if lv < level {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Msg+fmt.Sprint(r.Attrs)), q) {
			continue
		}
		out = append(out, r)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func secret(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"token", "password", "secret", "passphrase", "kek", "cookie", "authorization", "apikey", "api_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// Wrap returns a handler that keeps records and passes them on to h.
func Wrap(h slog.Handler) slog.Handler { return &handler{next: h} }

type handler struct {
	next   slog.Handler
	attrs  []slog.Attr
	groups string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	rec := Record{Time: r.Time, Level: r.Level.String(), Msg: r.Message}
	put := func(a slog.Attr) {
		if rec.Attrs == nil {
			rec.Attrs = map[string]string{}
		}
		k := h.groups + a.Key
		v := a.Value.Resolve().String()
		if secret(a.Key) {
			v = "[redacted]"
		}
		if len(v) > 2000 {
			v = v[:2000] + "…"
		}
		rec.Attrs[k] = v
	}
	for _, a := range h.attrs {
		put(a)
	}
	r.Attrs(func(a slog.Attr) bool { put(a); return true })
	add(rec)
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	return &handler{next: h.next.WithAttrs(as), attrs: append(append([]slog.Attr{}, h.attrs...), as...), groups: h.groups}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(name), attrs: h.attrs, groups: h.groups + name + "."}
}
