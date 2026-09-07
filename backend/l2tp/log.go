package l2tp

import (
	"fmt"
	"time"
)

const logTimestampFormat = "2006/01/02 15:04:05"

func (o *L2TP) emitLogf(severity, format string, args ...any) {
	o.emitLog(severity, fmt.Sprintf(format, args...))
}

func (o *L2TP) emitLog(severity, message string) {
	line := fmt.Sprintf("%s [%s] %s", time.Now().UTC().Format(logTimestampFormat), severity, message)
	select {
	case o.logChan <- line:
	default:
	}
}
