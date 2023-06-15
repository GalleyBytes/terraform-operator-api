package util

import "os"

func Tmpdir() string {
	t := os.TempDir()
	_, err := os.Stat(t)
	if err != nil {
		return "."
	}
	return t
}
