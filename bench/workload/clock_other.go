//go:build !windows

package main

import "time"

var clockStart = time.Now()

// nanotime returns a monotonic reading in nanoseconds.
func nanotime() int64 { return int64(time.Since(clockStart)) }
