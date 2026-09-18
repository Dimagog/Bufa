//go:build !windows

package vfsx

var UseCopyFileOS bool = false

func osCopyFile(_, _ string, _ bool) { panic("osCopyFile not implemented on this platform") }
