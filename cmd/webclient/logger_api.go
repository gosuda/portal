package main

// Logger abstracts the logging interface to allow swapping implementations
type Logger interface {
	Info() Event
	Warn() Event
	Error() Event
	Debug() Event
	Err(err error) Event
}

// Event abstracts the logging event to allow chaining
type Event interface {
	Str(key, val string) Event
	Strs(key string, val []string) Event
	Int(key string, val int) Event
	Bool(key string, val bool) Event
	Interface(key string, val interface{}) Event
	Err(err error) Event
	Msg(msg string)
	Msgf(format string, v ...interface{})
}
