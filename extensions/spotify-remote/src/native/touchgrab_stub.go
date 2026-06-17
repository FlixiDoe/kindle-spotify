//go:build !linux

package main

import "os"

func ioctlGrab(_ *os.File, _ bool) error {
	return nil
}
