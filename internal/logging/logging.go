// Package logging configures the default slog logger. One record per line:
//
//	2006-01-02 15:04:05.000 LEVEL message key=value ... (file:line)
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	c "github.com/dimagog/bufa/internal/contract"
)

// Level precedence: the level arg, then $LOG_LEVEL, then "none".
func Configure(level string, errOut io.Writer) {
	level = strings.TrimSpace(level)
	if level == "" {
		level = strings.TrimSpace(os.Getenv("LOG_LEVEL"))
	}
	if level == "" {
		level = "none"
	}
	lvl := parseLevel(level)
	h := &prettyHandler{out: errOut, mu: new(sync.Mutex), level: lvl, addSource: sourceEnabled()}
	slog.SetDefault(slog.New(h))
}

func sourceEnabled() bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("LOG_SOURCE")))
	return err == nil && v
}

// above slog.LevelError, so no record passes the handler filter
const levelNone = slog.LevelError + 1

// Display order lives in LevelNames; TestLevelNamesInSync keeps the two in sync.
var levels = map[string]slog.Level{
	"none":  levelNone,
	"error": slog.LevelError,
	"warn":  slog.LevelWarn,
	"info":  slog.LevelInfo,
	"debug": slog.LevelDebug,
}

// A constant, so nothing is computed at startup.
const LevelNames = "none, error, warn, info, debug"

func parseLevel(s string) slog.Level {
	if level, ok := levels[strings.ToLower(s)]; ok {
		return level
	}
	c.Fail("unknown --log-level '%s' (expect one of: %s)", s, LevelNames)
	return slog.LevelInfo
}

// WithAttrs/WithGroup are implemented for completeness, with one simplification: the group prefix
// is applied at format time to all attributes. mu is shared across With* clones.
type prettyHandler struct {
	out         io.Writer
	mu          *sync.Mutex
	level       slog.Leveler
	addSource   bool
	attrs       []slog.Attr
	groupPrefix string
}

func (h *prettyHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= h.level.Level()
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	b := make([]byte, 0, 256) // one line: timestamp+level+msg+attrs+source

	b = r.Time.AppendFormat(b, "2006-01-02 15:04:05.000")
	b = append(b, ' ')
	b = append(b, levelLabel(r.Level)...)
	b = append(b, ' ')
	b = append(b, r.Message...) // msg always raw

	for _, a := range h.attrs {
		b = appendAttr(b, h.groupPrefix, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		b = appendAttr(b, h.groupPrefix, a)
		return true
	})

	if h.addSource && r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		if f.File != "" {
			b = append(b, " ("...)
			b = append(b, f.File...)
			b = append(b, ':')
			b = strconv.AppendInt(b, int64(f.Line), 10)
			b = append(b, ')')
		}
	}
	b = append(b, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.out.Write(b)
	return err
}

func (h *prettyHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	clone := *h
	clone.attrs = make([]slog.Attr, 0, len(h.attrs)+len(as))
	clone.attrs = append(clone.attrs, h.attrs...)
	clone.attrs = append(clone.attrs, as...)
	return &clone
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groupPrefix = h.groupPrefix + name + "."
	return &clone
}

func appendAttr(b []byte, prefix string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return b
	}
	if a.Value.Kind() == slog.KindGroup {
		gs := a.Value.Group()
		if len(gs) == 0 {
			return b
		}
		np := prefix
		if a.Key != "" {
			np = prefix + a.Key + "."
		}
		for _, ga := range gs {
			b = appendAttr(b, np, ga)
		}
		return b
	}
	b = append(b, ' ')
	b = append(b, prefix...)
	b = append(b, a.Key...)
	b = append(b, '=')
	return appendValue(b, a.Value)
}

func appendValue(b []byte, v slog.Value) []byte {
	s := v.String()
	if needsQuote(s) {
		return append(b, strconv.Quote(s)...)
	}
	return append(b, s...)
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r <= ' ' || r == '=' || r == '"' {
			return true
		}
	}
	return false
}

// pre-padded to width 5, so the common path allocates nothing
const (
	labelDebug = "DEBUG"
	labelInfo  = "INFO "
	labelWarn  = "WARN "
	labelError = "ERROR"
)

// Non-standard levels fall back to slog's own label, which may be wider than 5.
func levelLabel(l slog.Level) string {
	switch l {
	case slog.LevelDebug:
		return labelDebug
	case slog.LevelInfo:
		return labelInfo
	case slog.LevelWarn:
		return labelWarn
	case slog.LevelError:
		return labelError
	default:
		return l.String()
	}
}
