//go:build linux

package main

import (
	"os"
	"syscall"
)

func ioctlGrab(f *os.File, grab bool) error {
	val := uintptr(0)
	if grab {
		val = 1
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(0x40044590), val)
	if errno != 0 {
		return errno
	}
	return nil
}
