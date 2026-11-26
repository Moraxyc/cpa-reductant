package engine

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/yetone/magpie/pkg/redact"
)

// StreamWriter abstracts redact.Writer so both unscoped and CallerScope-isolated
// stream writers share a single interface in the adapter and engine.
type StreamWriter interface {
	Header() http.Header
	WriteHeader(int)
	Write([]byte) (int, error)
	Finish()
	Put() map[string]int
}

// placeholderStore decouples stream restoration and scope isolation from
// Magpie's process-global placeholder registry.
type placeholderStore interface {
	NewWriter(w http.ResponseWriter) StreamWriter
	HasValue(placeholder string) bool
	LookupOriginal(placeholder string, shape bool) string
}

type magpiePlaceholderStore struct{}

func (magpiePlaceholderStore) NewWriter(w http.ResponseWriter) StreamWriter {
	return redact.NewWriter(w)
}

func (magpiePlaceholderStore) HasValue(placeholder string) bool {
	_, ok := redact.Value(placeholder)
	return ok
}

func (magpiePlaceholderStore) LookupOriginal(placeholder string, shape bool) string {
	return lookupOriginalValue(placeholder, shape)
}

func lookupOriginalValue(placeholder string, shape bool) string {
	if v, ok := redact.Value(placeholder); ok {
		return v
	}
	if shape {
		if idx := strings.IndexAny(placeholder, "=:"); idx >= 0 {
			restStart := idx + 1
			for restStart < len(placeholder) && (placeholder[restStart] == ' ' || placeholder[restStart] == '\t') {
				restStart++
			}
			if k, ok := redact.Value(placeholder[restStart:]); ok {
				return placeholder[:restStart] + k
			}
		}
	}
	return ""
}

type restoreBuffer struct {
	bytes.Buffer
	header http.Header
}

func (b *restoreBuffer) Header() http.Header { return b.header }
func (*restoreBuffer) WriteHeader(int)       {}
