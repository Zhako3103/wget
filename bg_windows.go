//go:build windows

package main

import "syscall"

// DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
}
