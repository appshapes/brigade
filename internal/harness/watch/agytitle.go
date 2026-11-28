package watch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/harness/config"
)

// agyAnnotationBytes caps the annotation file read: agy writes one short
// line there (measured on agy 1.5.19, 30 to 43 bytes).
const agyAnnotationBytes = 64 << 10

// AntigravityTitle is the sanitized title of one Antigravity CLI
// conversation, or "" when it has none yet (work item 544): agy's own
// generated title, or the name a /rename gave it. It reads only that
// conversation's annotation file; the conversation stores (SQLite) are
// never opened.
func AntigravityTitle(environ []string, id string) string {
	r := hostFactsReader{kind: config.HistoryAnnotations, root: config.HistoryAnnotations.Root(environ), id: id}
	return r.refresh().name
}

// annotations is Antigravity CLI's history (config.HistoryAnnotations):
// annotations/<conversation id>.pbtxt under ~/.gemini/antigravity-cli,
// read for the title alone. The file is filed by the id, so no launch
// directory is asked.
type annotations struct{}

func (annotations) refresh(r *hostFactsReader) localHostFacts {
	return r.refreshPinned(r.readAgyAnnotation)
}

func (annotations) filedUnder(*watcher) string { return "" }

// readAgyAnnotation brings the name up to date from the conversation's
// annotation file. A file that is gone, too large or holds no title
// leaves the last name: a title is never taken back to the folder's name
// once the roster showed one.
func (r *hostFactsReader) readAgyAnnotation(root *os.File) {
	f, fi := openHostPath(root, filepath.Join("annotations", r.id+".pbtxt"), false)
	if f == nil {
		r.logInfo = nil
		return
	}
	defer func() { _ = f.Close() }()
	if unchangedHostFile(r.logInfo, fi) {
		return
	}
	r.logInfo = fi
	if fi.Size() <= 0 || fi.Size() > agyAnnotationBytes {
		return
	}
	buf := make([]byte, fi.Size())
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	if name := oneLineName(agyTitle(string(buf[:n]))); name != "" {
		r.facts.name = name
	}
}

// agyTitle is the value of the top-level `title` field of a protobuf text
// document, "" when it has none or the value is not one double-quoted
// string. Text format's string escapes are C's, which strconv.Unquote
// reads (\" \\ \n, octal and hex bytes). A field inside a nested message
// is not top-level and is not read.
func agyTitle(doc string) string {
	depth := 0
	for line := range strings.SplitSeq(doc, "\n") {
		line = strings.TrimSpace(line)
		if depth == 0 {
			if rest, ok := strings.CutPrefix(line, "title"); ok {
				rest = strings.TrimSpace(rest)
				if rest, ok = strings.CutPrefix(rest, ":"); ok {
					if title, ok := quotedPrefix(strings.TrimSpace(rest)); ok {
						return title
					}
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			return ""
		}
	}
	return ""
}

// quotedPrefix unquotes the double-quoted string s begins with. A
// C escape Go does not take inside double quotes, \', is read as '.
func quotedPrefix(s string) (string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", false
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			if s[i] != '\'' {
				b.WriteByte('\\')
			}
			b.WriteByte(s[i])
		case c == '"':
			b.WriteByte('"')
			v, err := strconv.Unquote(b.String())
			return v, err == nil
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}
