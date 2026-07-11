//go:build windows

package player

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	potWMUser         = 0x0400
	potGetTotalTime   = 0x5002
	potGetCurrentTime = 0x5004
	potSetCurrentTime = 0x5005
	potGetPlayStatus  = 0x5006
	wmGetText         = 0x000D
	smtoBlock         = 0x0001
	smtoAbortIfHung   = 0x0002
	smtoErrorOnExit   = 0x0020
	messageTimeout    = 250 * time.Millisecond
	windowBindTimeout = 5 * time.Second
)

var sendMessageTimeoutW = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

type PotPlayerAdapter struct{}

func NewPotPlayerAdapter() *PotPlayerAdapter {
	return &PotPlayerAdapter{}
}

func (a *PotPlayerAdapter) Start(ctx context.Context, executable, filePath string) (LaunchResult, error) {
	executable = strings.TrimSpace(executable)
	if a == nil || executable == "" {
		return LaunchResult{}, ErrUnsupported
	}
	_, err := enumeratePotPlayerWindows()
	if err != nil {
		return LaunchResult{}, fmt.Errorf("enumerate PotPlayer windows: %w", err)
	}
	cmd := exec.Command(executable, "/new", filePath)
	cmd.Dir = filepath.Dir(filePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return LaunchResult{}, err
	}
	pid := uint32(cmd.Process.Pid)
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}

	deadline := time.NewTimer(windowBindTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return LaunchResult{}, ctx.Err()
		case <-deadline.C:
			return LaunchResult{Warning: errors.New("PotPlayer started, but Navi could not uniquely bind its new window; progress sync is disabled")}, nil
		case <-ticker.C:
			windowsNow, enumErr := enumeratePotPlayerWindows()
			if enumErr != nil {
				continue
			}
			var added []windows.HWND
			for _, candidate := range windowsNow {
				if candidate.pid == pid {
					added = append(added, candidate.handle)
				}
			}
			if len(added) == 1 {
				session := &potPlayerSession{handle: added[0], expectedFile: filepath.Base(filePath)}
				if _, probeErr := session.Sample(ctx); probeErr != nil && errors.Is(probeErr, ErrAccessDenied) {
					return LaunchResult{Warning: ErrAccessDenied}, nil
				}
				return LaunchResult{Session: session}, nil
			}
		}
	}
}

type potPlayerSession struct {
	handle       windows.HWND
	expectedFile string
	loaded       bool
}

func (s *potPlayerSession) Sample(_ context.Context) (Sample, error) {
	if !windows.IsWindow(s.handle) {
		return Sample{}, ErrWindowClosed
	}
	title, err := potWindowTitle(s.handle)
	if err != nil {
		return Sample{}, classifyWin32Error(err)
	}
	if !titleMatchesFile(title, s.expectedFile) {
		if strings.EqualFold(strings.TrimSpace(title), "PotPlayer") {
			if s.loaded {
				return Sample{State: StateStopped}, nil
			}
			return Sample{}, ErrNotLoaded
		}
		return Sample{}, ErrFileChanged
	}
	current, err := potMessage(s.handle, potGetCurrentTime, 0)
	if err != nil {
		return Sample{}, classifyWin32Error(err)
	}
	total, err := potMessage(s.handle, potGetTotalTime, 0)
	if err != nil {
		return Sample{}, classifyWin32Error(err)
	}
	status, err := potMessage(s.handle, potGetPlayStatus, 0)
	if err != nil {
		return Sample{}, classifyWin32Error(err)
	}
	titleAfter, err := potWindowTitle(s.handle)
	if err != nil {
		return Sample{}, classifyWin32Error(err)
	}
	if titleAfter != title || !titleMatchesFile(titleAfter, s.expectedFile) {
		return Sample{}, ErrFileChanged
	}
	statusCode := int32(status)
	if !s.loaded && statusCode == -1 && total == 0 {
		return Sample{}, ErrNotLoaded
	}
	s.loaded = true
	state := StateStopped
	switch statusCode {
	case 2:
		state = StatePlaying
	case 1:
		state = StatePaused
	}
	return Sample{Position: time.Duration(current) * time.Millisecond, Duration: time.Duration(total) * time.Millisecond, State: state}, nil
}

func (s *potPlayerSession) Seek(_ context.Context, position time.Duration) error {
	if !windows.IsWindow(s.handle) {
		return ErrWindowClosed
	}
	_, err := potMessage(s.handle, potSetCurrentTime, uintptr(position/time.Millisecond))
	return classifyWin32Error(err)
}

func (*potPlayerSession) Detach() error { return nil }

type potWindow struct {
	handle windows.HWND
	pid    uint32
}

func enumeratePotPlayerWindows() ([]potWindow, error) {
	var found []potWindow
	callback := windows.NewCallback(func(rawHandle uintptr, _ uintptr) uintptr {
		handle := windows.HWND(rawHandle)
		buffer := make([]uint16, 256)
		copied, err := windows.GetClassName(handle, &buffer[0], int32(len(buffer)))
		if err == nil {
			className := windows.UTF16ToString(buffer[:copied])
			if className == "PotPlayer64" || className == "PotPlayer" {
				var pid uint32
				if _, pidErr := windows.GetWindowThreadProcessId(handle, &pid); pidErr == nil {
					found = append(found, potWindow{handle: handle, pid: pid})
				}
			}
		}
		return 1
	})
	if err := windows.EnumWindows(callback, nil); err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool { return uintptr(found[i].handle) < uintptr(found[j].handle) })
	return found, nil
}

func potWindowTitle(handle windows.HWND) (string, error) {
	buffer := make([]uint16, 4096)
	copied, err := sendTimeout(handle, wmGetText, uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])))
	if err != nil {
		return "", err
	}
	if copied >= uintptr(len(buffer)) {
		copied = uintptr(len(buffer) - 1)
	}
	return windows.UTF16ToString(buffer[:copied]), nil
}

func potMessage(handle windows.HWND, operation, value uintptr) (uintptr, error) {
	return sendTimeout(handle, potWMUser, operation, value)
}

func sendTimeout(handle windows.HWND, message uint32, wParam, lParam uintptr) (uintptr, error) {
	var result uintptr
	returned, _, callErr := sendMessageTimeoutW.Call(uintptr(handle), uintptr(message), wParam, lParam,
		uintptr(smtoBlock|smtoAbortIfHung|smtoErrorOnExit), uintptr(messageTimeout/time.Millisecond), uintptr(unsafe.Pointer(&result)))
	if returned != 0 {
		return result, nil
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == 0 {
		return 0, ErrIPCTimeout
	}
	return 0, callErr
}

func classifyWin32Error(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return ErrAccessDenied
	}
	if errors.Is(err, windows.ERROR_TIMEOUT) {
		return ErrIPCTimeout
	}
	return err
}

func titleMatchesFile(title, expectedFile string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	expectedFile = strings.ToLower(strings.TrimSpace(expectedFile))
	if title == "" || expectedFile == "" {
		return false
	}
	stem := strings.TrimSuffix(expectedFile, filepath.Ext(expectedFile))
	return title == expectedFile || title == stem || strings.HasPrefix(title, expectedFile+" - ") || (stem != "" && strings.HasPrefix(title, stem+" - "))
}
