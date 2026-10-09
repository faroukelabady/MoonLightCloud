//go:build !linux

package main

import "errors"

// readNoEcho is unavailable off Linux: use --password-stdin/--password-file.
func readNoEcho(string) (string, error) {
	return "", errors.New("no-echo terminal input is supported on Linux only")
}
