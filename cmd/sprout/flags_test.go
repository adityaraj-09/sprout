package main

import (
	"testing"
)

func TestNormalizeFormat(t *testing.T) {
	if normalizeFormat("JSON") != "json" {
		t.Fatal("json")
	}
	if normalizeFormat("text") != "text" {
		t.Fatal("text")
	}
}

func TestConnStringOf(t *testing.T) {
	if connStringOf(map[string]any{"connection_string": "postgres://x"}) != "postgres://x" {
		t.Fatal("map")
	}
}
