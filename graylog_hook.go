package graylog // import "github.com/raphyot/logrus-graylog-hook"

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// Set graylog.BufSize = <value> _before_ calling NewGraylogHook
// Once the buffer is full, logging will start blocking, waiting for slots to
// be available in the queue.
var BufSize uint = 8192

// GraylogHook to send logs to a logging service compatible with the Graylog API and the GELF format.
type GraylogHook struct {
	Facility   string
	Extra      map[string]interface{}
	gelfLogger *Writer
	buf        chan graylogEntry
	blacklist  map[string]bool
}

// Graylog needs file and line params
type graylogEntry struct {
	*logrus.Entry
	file string
	line int
}

// NewGraylogHook creates a hook to be added to an instance of logger.
func NewGraylogHook(addr string, facility string, extra map[string]interface{}) *GraylogHook {
	g, err := NewWriter(addr)
	if err != nil {
		logrus.WithField("err", err).Info("Can't create Gelf logger")
	}
	hook := &GraylogHook{
		Facility:   facility,
		Extra:      extra,
		gelfLogger: g,
		buf:        make(chan graylogEntry, BufSize),
	}
	go hook.fire() // Log in background
	return hook
}

// Fire is called when a log event is fired.
// We assume the entry will be altered by another hook,
// otherwise we might logging something wrong to Graylog
func (hook *GraylogHook) Fire(entry *logrus.Entry) error {
	// get caller file and line here, it won't be available inside the goroutine
	// 1 for the function that called us.
	file, line := getCallerIgnoringLogMulti(1)

	gEntry := graylogEntry{entry, file, line}
	// Don't exit before sending the message
	if entry.Level == logrus.FatalLevel || entry.Level == logrus.PanicLevel {
		hook.sendEntry(gEntry)
		return nil

	}
	hook.buf <- gEntry
	return nil
}

// fire will loop on the 'buf' channel, and write entries to graylog
func (hook *GraylogHook) fire() {
	for {
		entry := <-hook.buf // receive new entry on channel
		hook.sendEntry(entry)
	}
}

// sendEntry sends an entry to graylog synchronously
func (hook *GraylogHook) sendEntry(entry graylogEntry) {
	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}

	w := hook.gelfLogger

	// remove trailing and leading whitespace
	p := bytes.TrimSpace([]byte(entry.Message))

	// If there are newlines in the message, use the first line
	// for the short message and set the full message to the
	// original input.  If the input has no newlines, stick the
	// whole thing in Short.
	short := p
	full := []byte("")
	if i := bytes.IndexRune(p, '\n'); i > 0 {
		short = p[:i]
		full = p
	}

	level := int32(entry.Level) + 2 // logrus levels are lower than syslog by 2

	// Don't modify entry.Data directly, as the entry will used after this hook was fired
	extra := map[string]interface{}{}
	// Merge extra fields
	for k, v := range hook.Extra {
		k = fmt.Sprintf("_%s", k) // "[...] every field you send and prefix with a _ (underscore) will be treated as an additional field."
		extra[k] = v
	}
	for k, v := range entry.Data {
		if !hook.blacklist[k] {
			k = fmt.Sprintf("_%s", k) // "[...] every field you send and prefix with a _ (underscore) will be treated as an additional field."
			extra[k] = v
		}
	}

	m := Message{
		Version:  "1.1",
		Host:     host,
		Short:    string(short),
		Full:     string(full),
		TimeUnix: float64(time.Now().UnixNano()/1000000) / 1000.,
		Level:    level,
		Facility: hook.Facility,
		File:     entry.file,
		Line:     entry.line,
		Extra:    extra,
	}

	if err := w.WriteMessage(&m); err != nil {
		fmt.Println(err)
	}

}

// Levels returns the available logging levels.
func (hook *GraylogHook) Levels() []logrus.Level {
	return []logrus.Level{
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
		logrus.DebugLevel,
	}
}

// Blacklist create a blacklist map to filter some message keys.
// This useful when you want your application to log extra fields locally
// but don't want graylog to store them.
func (hook *GraylogHook) Blacklist(b []string) {
	hook.blacklist = make(map[string]bool)
	for _, elem := range b {
		hook.blacklist[elem] = true
	}
}

// getCaller returns the filename and the line info of a function
// further down in the call stack.  Passing 0 in as callDepth would
// return info on the function calling getCallerIgnoringLog, 1 the
// parent function, and so on.  Any suffixes passed to getCaller are
// path fragments like "/pkg/log/log.go", and functions in the call
// stack from that file are ignored.
func getCaller(callDepth int, suffixesToIgnore ...string) (file string, line int) {
	// bump by 1 to ignore the getCaller (this) stackframe
	callDepth++
outer:
	for {
		var ok bool
		_, file, line, ok = runtime.Caller(callDepth)
		if !ok {
			file = "???"
			line = 0
			break
		}

		for _, s := range suffixesToIgnore {
			if strings.HasSuffix(file, s) {
				callDepth++
				continue outer
			}
		}
		break
	}
	return
}

func getCallerIgnoringLogMulti(callDepth int) (string, int) {
	// the +1 is to ignore this (getCallerIgnoringLogMulti) frame
	return getCaller(callDepth+1, "logrus/hooks.go", "logrus/entry.go", "logrus/logger.go", "logrus/exported.go", "asm_amd64.s")
}
