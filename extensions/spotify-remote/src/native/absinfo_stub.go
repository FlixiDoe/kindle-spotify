//go:build !linux

package main

import "os"

func queryInputAbsCalibration(_ *os.File) (touchCalibration, bool) {
	return touchCalibration{}, false
}
