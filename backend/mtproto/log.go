package mtproto

import (
	"fmt"
	"strings"

	"github.com/9seconds/mtg/v2/mtglib"
)

// recordLog feeds a line into the shared logsChan consumed by Logs(),
// non-blocking (mirrors backend/openvpn/log.go's recordLog) - skips if the
// channel is full rather than stalling whatever goroutine is logging.
func (b *Backend) recordLog(line string) {
	select {
	case b.logsChan <- line:
	default:
	}
}

// mtgLogger adapts this package's plain-string logging (recordLog) to
// mtglib.Logger's structured interface. Each Named/Bind* call returns an
// independent copy so parent loggers are never mutated by children, matching
// mtglib.Logger's documented contract.
type mtgLogger struct {
	name   string
	fields []string
	out    func(string)
}

func newMtgLogger(name string, out func(string)) *mtgLogger {
	return &mtgLogger{name: name, out: out}
}

func (l *mtgLogger) clone() *mtgLogger {
	cp := *l
	cp.fields = append([]string(nil), l.fields...)
	return &cp
}

func (l *mtgLogger) Named(name string) mtglib.Logger {
	c := l.clone()
	if c.name != "" {
		c.name = c.name + "." + name
	} else {
		c.name = name
	}
	return c
}

func (l *mtgLogger) BindInt(name string, value int) mtglib.Logger {
	c := l.clone()
	c.fields = append(c.fields, fmt.Sprintf("%s=%d", name, value))
	return c
}

func (l *mtgLogger) BindStr(name, value string) mtglib.Logger {
	c := l.clone()
	c.fields = append(c.fields, fmt.Sprintf("%s=%s", name, value))
	return c
}

func (l *mtgLogger) BindJSON(name, value string) mtglib.Logger {
	return l.BindStr(name, value)
}

func (l *mtgLogger) Printf(format string, args ...any) {
	l.emit(fmt.Sprintf(format, args...))
}

func (l *mtgLogger) Info(msg string) {
	l.emit(msg)
}

func (l *mtgLogger) InfoError(msg string, err error) {
	l.emit(withErr(msg, err))
}

func (l *mtgLogger) Warning(msg string) {
	l.emit(msg)
}

func (l *mtgLogger) WarningError(msg string, err error) {
	l.emit(withErr(msg, err))
}

func (l *mtgLogger) Debug(msg string) {
	l.emit(msg)
}

func (l *mtgLogger) DebugError(msg string, err error) {
	l.emit(withErr(msg, err))
}

// withErr defensively handles a nil err, rather than trusting every caller
// (including mtglib itself) to never pass one to an *Error logging method.
func withErr(msg string, err error) string {
	if err == nil {
		return msg
	}
	return msg + ": " + err.Error()
}

func (l *mtgLogger) emit(msg string) {
	line := msg
	if l.name != "" {
		line = "[" + l.name + "] " + line
	}
	if len(l.fields) > 0 {
		line += " (" + strings.Join(l.fields, " ") + ")"
	}
	l.out(line)
}
