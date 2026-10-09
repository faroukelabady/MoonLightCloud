//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// readNoEcho reads one line from the controlling terminal with echo off.
func readNoEcho(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", err
	}
	silent := *state
	silent.Lflag &^= unix.ECHO
	silent.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &silent); err != nil {
		return "", err
	}
	defer func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, state) }()
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
