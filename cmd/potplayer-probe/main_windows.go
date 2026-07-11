//go:build windows

// potplayer-probe is a manual, Windows-only diagnostic for the candidate
// PotPlayer WM_USER protocol. It is intentionally isolated from Navi's app,
// database, and player-launch code.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	candidateWMUser         = 0x0400
	candidateGetTotalTime   = 0x5002
	candidateGetCurrentTime = 0x5004
	candidateSetCurrentTime = 0x5005
	candidateGetPlayStatus  = 0x5006
	wmGetText               = 0x000D
	smtoBlock               = 0x0001
	smtoAbortIfHung         = 0x0002
	smtoErrorOnExit         = 0x0020
	messageTimeout          = 250 * time.Millisecond
	titleBufferCharacters   = 4096
	processPathBufferChars  = windows.MAX_LONG_PATH
	seekForwardMilliseconds = uint64(10_000)
	sampleInterval          = time.Second
)

var sendMessageTimeoutW = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

type potPlayerWindow struct {
	Handle      windows.HWND
	ClassName   string
	PID         uint32
	ProcessPath string
	PathErr     error
	Title       string
	TitleErr    error
}

type candidateResult struct {
	value uintptr
	err   error
}

type playbackSample struct {
	current candidateResult
	total   candidateResult
	status  candidateResult
}

func main() {
	windowIndex := flag.Int("window", 0, "1-based candidate window index; omit to select interactively")
	flag.Parse()

	if err := run(os.Stdin, os.Stdout, *windowIndex); err != nil {
		fmt.Fprintln(os.Stderr, "potplayer-probe:", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer, requestedWindow int) error {
	scanner := bufio.NewScanner(in)
	windows, err := enumeratePotPlayerWindows()
	if err != nil {
		return err
	}
	printWindows(out, windows)
	if len(windows) == 0 {
		fmt.Fprintln(out, "No top-level windows with class PotPlayer64 or PotPlayer were found.")
		return nil
	}

	selected, err := selectWindow(scanner, out, windows, requestedWindow)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return monitorWindow(ctx, scanner, out, selected)
}

func enumeratePotPlayerWindows() ([]potPlayerWindow, error) {
	var found []potPlayerWindow
	callback := windows.NewCallback(func(rawHandle uintptr, _ uintptr) uintptr {
		handle := windows.HWND(rawHandle)
		className, err := windowClassName(handle)
		if err != nil || !isPotPlayerClass(className) {
			return 1
		}

		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(handle, &pid); err != nil {
			return 1
		}

		processPath, pathErr := processImagePath(pid)
		title, titleErr := windowTitle(handle)
		found = append(found, potPlayerWindow{
			Handle:      handle,
			ClassName:   className,
			PID:         pid,
			ProcessPath: processPath,
			PathErr:     pathErr,
			Title:       title,
			TitleErr:    titleErr,
		})
		return 1
	})

	if err := windows.EnumWindows(callback, nil); err != nil {
		return nil, fmt.Errorf("enumerate top-level windows: %w", err)
	}
	sort.Slice(found, func(i, j int) bool {
		return uintptr(found[i].Handle) < uintptr(found[j].Handle)
	})
	return found, nil
}

func isPotPlayerClass(className string) bool {
	return className == "PotPlayer64" || className == "PotPlayer"
}

func windowClassName(handle windows.HWND) (string, error) {
	buffer := make([]uint16, 256)
	copied, err := windows.GetClassName(handle, &buffer[0], int32(len(buffer)))
	if err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:copied]), nil
}

func processImagePath(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)

	buffer := make([]uint16, processPathBufferChars)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func windowTitle(handle windows.HWND) (string, error) {
	buffer := make([]uint16, titleBufferCharacters)
	copied, err := sendMessageTimeout(handle, wmGetText, uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])))
	if err != nil {
		return "", err
	}
	if copied >= uintptr(len(buffer)) {
		copied = uintptr(len(buffer) - 1)
	}
	return windows.UTF16ToString(buffer[:copied]), nil
}

func sendMessageTimeout(handle windows.HWND, message uint32, wParam uintptr, lParam uintptr) (uintptr, error) {
	var result uintptr
	returned, _, callErr := sendMessageTimeoutW.Call(
		uintptr(handle),
		uintptr(message),
		wParam,
		lParam,
		uintptr(smtoBlock|smtoAbortIfHung|smtoErrorOnExit),
		uintptr(messageTimeout/time.Millisecond),
		uintptr(unsafe.Pointer(&result)),
	)
	if returned != 0 {
		return result, nil
	}
	if err := nonZeroWindowsError(callErr); err != nil {
		return 0, fmt.Errorf("SendMessageTimeoutW: %w", err)
	}
	return 0, errors.New("SendMessageTimeoutW returned FALSE without a Win32 error")
}

func nonZeroWindowsError(err error) error {
	if err == nil {
		return nil
	}
	if errno, ok := err.(syscall.Errno); ok && errno == 0 {
		return nil
	}
	return err
}

func printWindows(out io.Writer, candidates []potPlayerWindow) {
	fmt.Fprintln(out, "PotPlayer candidate top-level windows:")
	for index, candidate := range candidates {
		processPath := candidate.ProcessPath
		if candidate.PathErr != nil {
			processPath = "<unavailable: " + formatWindowsError(candidate.PathErr) + ">"
		}
		title := candidate.Title
		if candidate.TitleErr != nil {
			title = "<unavailable: " + formatWindowsError(candidate.TitleErr) + ">"
		}
		fmt.Fprintf(out, "[%d] class=%q HWND=0x%X PID=%d process_path=%q title=%q\n", index+1, candidate.ClassName, uintptr(candidate.Handle), candidate.PID, processPath, title)
	}
}

func selectWindow(scanner *bufio.Scanner, out io.Writer, candidates []potPlayerWindow, requestedWindow int) (potPlayerWindow, error) {
	if requestedWindow > 0 {
		return windowBySelection(candidates, requestedWindow)
	}

	fmt.Fprintf(out, "Select a window [1-%d]: ", len(candidates))
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return potPlayerWindow{}, fmt.Errorf("read window selection: %w", err)
		}
		return potPlayerWindow{}, errors.New("window selection is required")
	}
	selection, err := parseSelection(scanner.Text())
	if err != nil {
		return potPlayerWindow{}, err
	}
	return windowBySelection(candidates, selection)
}

func parseSelection(input string) (int, error) {
	selection, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || selection < 1 {
		return 0, fmt.Errorf("selection must be a positive window index, got %q", input)
	}
	return selection, nil
}

func windowBySelection(candidates []potPlayerWindow, selection int) (potPlayerWindow, error) {
	if selection < 1 || selection > len(candidates) {
		return potPlayerWindow{}, fmt.Errorf("selection %d is outside [1-%d]", selection, len(candidates))
	}
	return candidates[selection-1], nil
}

func monitorWindow(ctx context.Context, scanner *bufio.Scanner, out io.Writer, selected potPlayerWindow) error {
	fmt.Fprintln(out, "Sampling candidate WM_USER responses once per second. Values are raw candidates; time units and status meanings are unverified.")
	fmt.Fprintln(out, "Type seek to request a +10 second candidate seek, then type exact YES to execute it. Type quit to stop.")

	commands := readCommands(scanner)
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	awaitingSeekConfirmation := false

	for {
		if !windows.IsWindow(selected.Handle) {
			fmt.Fprintf(out, "Selected HWND=0x%X no longer exists.\n", uintptr(selected.Handle))
			return nil
		}
		printSample(out, samplePlayback(selected.Handle))

		select {
		case <-ctx.Done():
			return nil
		case command, ok := <-commands:
			if !ok {
				commands = nil
				continue
			}
			switch {
			case awaitingSeekConfirmation && command == "YES":
				awaitingSeekConfirmation = false
				performConfirmedSeek(out, selected.Handle)
			case awaitingSeekConfirmation:
				awaitingSeekConfirmation = false
				fmt.Fprintln(out, "Seek cancelled; exact YES was not received.")
			case command == "seek":
				awaitingSeekConfirmation = true
				fmt.Fprintln(out, "Candidate seek changes PotPlayer state. Type exact YES to move forward by up to 10000 candidate time units.")
			case command == "quit":
				return nil
			case command != "":
				fmt.Fprintln(out, "Unknown command. Use seek or quit.")
			}
		case <-ticker.C:
		}
	}
}

func readCommands(scanner *bufio.Scanner) <-chan string {
	commands := make(chan string)
	go func() {
		defer close(commands)
		for scanner.Scan() {
			commands <- strings.TrimSpace(scanner.Text())
		}
	}()
	return commands
}

func samplePlayback(handle windows.HWND) playbackSample {
	return playbackSample{
		current: candidateMessage(handle, candidateGetCurrentTime, 0),
		total:   candidateMessage(handle, candidateGetTotalTime, 0),
		status:  candidateMessage(handle, candidateGetPlayStatus, 0),
	}
}

func candidateMessage(handle windows.HWND, operation uintptr, value uintptr) candidateResult {
	result, err := sendMessageTimeout(handle, candidateWMUser, operation, value)
	return candidateResult{value: result, err: err}
}

func printSample(out io.Writer, sample playbackSample) {
	fmt.Fprintf(out, "%s current_time_ms=%s total_time_ms=%s play_status=%s\n",
		time.Now().Format(time.RFC3339),
		formatCandidateTime(sample.current),
		formatCandidateTime(sample.total),
		formatCandidateStatus(sample.status),
	)
}

func formatCandidateTime(result candidateResult) string {
	if result.err != nil {
		return "ERROR(" + formatWindowsError(result.err) + ")"
	}
	return fmt.Sprintf("%d (candidate unit unverified; raw=0x%X)", uint32(result.value), result.value)
}

func formatCandidateStatus(result candidateResult) string {
	if result.err != nil {
		return "ERROR(" + formatWindowsError(result.err) + ")"
	}
	return fmt.Sprintf("%d (raw=0x%X; meaning unverified)", signedStatus(result.value), result.value)
}

func signedStatus(value uintptr) int32 {
	return int32(uint32(value))
}

func performConfirmedSeek(out io.Writer, handle windows.HWND) {
	current := candidateMessage(handle, candidateGetCurrentTime, 0)
	if current.err != nil {
		fmt.Fprintln(out, "Seek was not sent: current_time_ms=ERROR("+formatWindowsError(current.err)+")")
		return
	}
	total := candidateMessage(handle, candidateGetTotalTime, 0)
	totalMilliseconds := uint64(0)
	if total.err == nil {
		totalMilliseconds = uint64(uint32(total.value))
	}
	target, ok := seekTarget(uint64(uint32(current.value)), totalMilliseconds)
	if !ok {
		fmt.Fprintln(out, "Seek was not sent: no forward +10 second candidate target is available.")
		return
	}

	result := candidateMessage(handle, candidateSetCurrentTime, uintptr(target))
	if result.err != nil {
		fmt.Fprintln(out, "Candidate seek failed: "+formatWindowsError(result.err))
		return
	}
	fmt.Fprintf(out, "Candidate seek sent: before=%d target=%d set_result=0x%X\n", uint32(current.value), target, result.value)
	time.Sleep(500 * time.Millisecond)
	printSample(out, samplePlayback(handle))
}

func seekTarget(current uint64, total uint64) (uint64, bool) {
	if total > 0 && current >= total {
		return 0, false
	}
	if current > math.MaxUint64-seekForwardMilliseconds {
		return 0, false
	}
	target := current + seekForwardMilliseconds
	if total > 0 && target > total {
		target = total
	}
	return target, target > current
}

func formatWindowsError(err error) string {
	switch {
	case errors.Is(err, windows.ERROR_TIMEOUT):
		return "timeout"
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return "access_denied"
	default:
		return err.Error()
	}
}
