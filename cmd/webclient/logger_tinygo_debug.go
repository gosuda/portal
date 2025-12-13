//go:build debug && tinygo

package main

import (
	"fmt"
)

func init() {
}

var log Logger = &tinygoLogger{}

type tinygoLogger struct{}

func (l *tinygoLogger) Info() Event         { return &tinygoEvent{level: "INFO"} }
func (l *tinygoLogger) Warn() Event         { return &tinygoEvent{level: "WARN"} }
func (l *tinygoLogger) Error() Event        { return &tinygoEvent{level: "ERROR"} }
func (l *tinygoLogger) Debug() Event        { return &tinygoEvent{level: "DEBUG"} }
func (l *tinygoLogger) Err(err error) Event { return &tinygoEvent{level: "ERROR", err: err} }

type tinygoEvent struct {
	level string
	err   error
	msg   string
}

func (e *tinygoEvent) Str(key, val string) Event { e.msg += fmt.Sprintf(" %s=%s", key, val); return e }
func (e *tinygoEvent) Strs(key string, val []string) Event {
	e.msg += fmt.Sprintf(" %s=%v", key, val)
	return e
}
func (e *tinygoEvent) Int(key string, val int) Event {
	e.msg += fmt.Sprintf(" %s=%d", key, val)
	return e
}
func (e *tinygoEvent) Bool(key string, val bool) Event {
	e.msg += fmt.Sprintf(" %s=%v", key, val)
	return e
}
func (e *tinygoEvent) Interface(key string, val interface{}) Event {
	e.msg += fmt.Sprintf(" %s=%v", key, val)
	return e
}
func (e *tinygoEvent) Err(err error) Event { e.err = err; return e }

func (e *tinygoEvent) Msg(msg string) {
	if e.err != nil {
		fmt.Printf("[%s] %s: %v %s\n", e.level, msg, e.err, e.msg)
	} else {
		fmt.Printf("[%s] %s %s\n", e.level, msg, e.msg)
	}
}

func (e *tinygoEvent) Msgf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if e.err != nil {
		fmt.Printf("[%s] %s: %v %s\n", e.level, msg, e.err, e.msg)
	} else {
		fmt.Printf("[%s] %s %s\n", e.level, msg, e.msg)
	}
}
