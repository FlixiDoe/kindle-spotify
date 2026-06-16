//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	absX           = 0x00
	absY           = 0x01
	absMTPositionX = 0x35
	absMTPositionY = 0x36

	iocNRShift   = 0
	iocTypeShift = 8
	iocSizeShift = 16
	iocDirShift  = 30
	iocRead      = 2
)

type inputAbsInfo struct {
	Value      int32
	Minimum    int32
	Maximum    int32
	Fuzz       int32
	Flat       int32
	Resolution int32
}

func queryInputAbsCalibration(f *os.File) (touchCalibration, bool) {
	if cal, ok := queryInputAbsPair(f, absMTPositionX, absMTPositionY, "kernel-abs-mt"); ok {
		return cal, true
	}
	return queryInputAbsPair(f, absX, absY, "kernel-abs")
}

func queryInputAbsPair(f *os.File, xCode, yCode uint16, source string) (touchCalibration, bool) {
	x, okX := queryInputAbsInfo(f, xCode)
	y, okY := queryInputAbsInfo(f, yCode)
	if !okX || !okY || x.Maximum <= x.Minimum || y.Maximum <= y.Minimum {
		return touchCalibration{}, false
	}
	return touchCalibration{
		MinX:   int(x.Minimum),
		MaxX:   int(x.Maximum),
		MinY:   int(y.Minimum),
		MaxY:   int(y.Maximum),
		Source: source,
	}, true
}

func queryInputAbsInfo(f *os.File, code uint16) (inputAbsInfo, bool) {
	var info inputAbsInfo
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), evioCGAbs(code), uintptr(unsafe.Pointer(&info)))
	return info, errno == 0
}

func evioCGAbs(code uint16) uintptr {
	return (uintptr(iocRead) << iocDirShift) |
		(uintptr(unsafe.Sizeof(inputAbsInfo{})) << iocSizeShift) |
		(uintptr('E') << iocTypeShift) |
		(uintptr(0x40+code) << iocNRShift)
}
