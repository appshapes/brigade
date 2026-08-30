//go:build darwin

package main

import "golang.org/x/sys/unix"

const ioctlTermios = unix.TIOCGETA
