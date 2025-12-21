//go:build !debug

package main

var log Logger = &noopLogger{}

type noopLogger struct{}

func (l *noopLogger) Info() Event         { return noopEvent }
func (l *noopLogger) Warn() Event         { return noopEvent }
func (l *noopLogger) Error() Event        { return noopEvent }
func (l *noopLogger) Debug() Event        { return noopEvent }
func (l *noopLogger) Err(err error) Event { return noopEvent }

var noopEvent = &noopEventImpl{}

type noopEventImpl struct{}

func (e *noopEventImpl) Str(key, val string) Event                   { return e }
func (e *noopEventImpl) Strs(key string, val []string) Event         { return e }
func (e *noopEventImpl) Int(key string, val int) Event               { return e }
func (e *noopEventImpl) Bool(key string, val bool) Event             { return e }
func (e *noopEventImpl) Interface(key string, val interface{}) Event { return e }
func (e *noopEventImpl) Err(err error) Event                         { return e }
func (e *noopEventImpl) Msg(msg string)                              {}
func (e *noopEventImpl) Msgf(format string, v ...interface{})        {}
