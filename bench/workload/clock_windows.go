//go:build windows

package main

import (
	"sync"
	"syscall"
	"unsafe"
)

// time.Now() on Windows follows the system timer, which other programs can
// hold at about 1 ms, so anything faster reads as zero.
// QueryPerformanceCounter ticks at 100 ns or better.
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	procQPC  = kernel32.NewProc("QueryPerformanceCounter")
	procQPF  = kernel32.NewProc("QueryPerformanceFrequency")

	qpcOnce sync.Once
	qpcFreq int64
)

// nanotime returns a monotonic reading in nanoseconds.
func nanotime() int64 {
	qpcOnce.Do(func() { procQPF.Call(uintptr(unsafe.Pointer(&qpcFreq))) })

	var c int64
	procQPC.Call(uintptr(unsafe.Pointer(&c)))

	// split so c*1e9 can't overflow
	return c/qpcFreq*1e9 + c%qpcFreq*1e9/qpcFreq
}
