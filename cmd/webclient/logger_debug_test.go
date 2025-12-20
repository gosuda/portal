//go:build js && wasm && debug && !tinygo

package main

import "testing"

func TestDebugLogger(t *testing.T) {
	if log == nil {
		t.Fatalf("expected logger")
	}
	log.Info().Str("k", "v").Int("i", 1).Bool("b", true).Msg("info")
	log.Warn().Strs("s", []string{"a", "b"}).Msg("warn")
	log.Error().Interface("x", map[string]int{"a": 1}).Msg("error")
	log.Debug().Msgf("debug %d", 1)
	log.Err(nil).Msg("err")
}
