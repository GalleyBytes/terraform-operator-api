package util

import (
	"os"
	"reflect"
)

func Tmpdir() string {
	t := os.TempDir()
	_, err := os.Stat(t)
	if err != nil {
		return "."
	}
	return t
}

// Contains is a very slow containment check for an item in a list
func Contains[T any](things []T, thing T) bool {
	for _, t := range things {
		if reflect.DeepEqual(thing, t) {
			return true
		}
	}
	return false
}
