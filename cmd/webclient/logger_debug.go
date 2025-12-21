//go:build debug && !tinygo

package main

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
)

func init() {
	zlog.Logger = zlog.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
}

var log Logger = &debugLogger{}

type debugLogger struct{}

func (l *debugLogger) Info() Event         { return &debugEvent{zlog.Info()} }
func (l *debugLogger) Warn() Event         { return &debugEvent{zlog.Warn()} }
func (l *debugLogger) Error() Event        { return &debugEvent{zlog.Error()} }
func (l *debugLogger) Debug() Event        { return &debugEvent{zlog.Debug()} }
func (l *debugLogger) Err(err error) Event { return &debugEvent{zlog.Error().Err(err)} }

type debugEvent struct {
	e *zerolog.Event
}

func (e *debugEvent) Str(key, val string) Event                   { e.e.Str(key, val); return e }
func (e *debugEvent) Strs(key string, val []string) Event         { e.e.Strs(key, val); return e }
func (e *debugEvent) Int(key string, val int) Event               { e.e.Int(key, val); return e }
func (e *debugEvent) Bool(key string, val bool) Event             { e.e.Bool(key, val); return e }
func (e *debugEvent) Interface(key string, val interface{}) Event { e.e.Interface(key, val); return e }
func (e *debugEvent) Err(err error) Event                         { e.e.Err(err); return e }
func (e *debugEvent) Msg(msg string)                              { e.e.Msg(msg) }
func (e *debugEvent) Msgf(format string, v ...interface{})        { e.e.Msgf(format, v...) }
