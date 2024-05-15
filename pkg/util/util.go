package util

import (
	"encoding/base64"
	"os"
	"reflect"
	"strings"

	"golang.org/x/crypto/bcrypt"
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

// Truncate an already valid resource name
// With valid resource names, it is safe to assume only contains dashes (dots?)
func Trunc(s string, n uint) string {
	if len(s) > int(n) {
		s = s[:n]
		// TODO fix naive approach of sanitizing
		s = strings.TrimRight(s, "-")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func B64Encode(input string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(input))
	return encoded
}

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
