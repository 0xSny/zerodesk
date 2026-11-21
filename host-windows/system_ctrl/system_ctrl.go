package system_ctrl

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
	"zerodesk-host/input"
)

var (
	powrprof             = windows.NewLazySystemDLL("powrprof.dll")
	procSetSuspendState  = powrprof.NewProc("SetSuspendState")
)

const (
	vkVolumeMute uint16 = 0xAD
	vkVolumeDown uint16 = 0xAE
	vkVolumeUp   uint16 = 0xAF
)

// VolumeMute toggles system audio mute.
func VolumeMute() error {
	return input.SendVirtualKey(vkVolumeMute, false)
}

// VolumeUp increases system volume.
func VolumeUp() error {
	return input.SendVirtualKey(vkVolumeUp, false)
}

// VolumeDown decreases system volume.
func VolumeDown() error {
	return input.SendVirtualKey(vkVolumeDown, false)
}

// Sleep puts the PC into S3/Modern Standby sleep immediately via powrprof!SetSuspendState.
func Sleep() error {
	// BOOLEAN SetSuspendState(BOOLEAN Hibernate, BOOLEAN ForceCritical, BOOLEAN DisableWakeEvent)
	ret, _, err := procSetSuspendState.Call(0, 0, 0)
	if ret == 0 {
		return fmt.Errorf("SetSuspendState failed: %w", err)
	}
	return nil
}

// ScheduleShutdown schedules a safe countdown shutdown (in seconds, min 10s).
func ScheduleShutdown(seconds int) error {
	if seconds < 10 {
		seconds = 10
	}
	if seconds > 86400 {
		seconds = 86400
	}
	cmd := exec.Command("shutdown.exe", "/s", "/t", strconv.Itoa(seconds))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("shutdown schedule failed: %w (%s)", err, string(out))
	}
	return nil
}

// CancelShutdown cancels any pending Windows shutdown timer.
func CancelShutdown() error {
	cmd := exec.Command("shutdown.exe", "/a")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("shutdown cancel failed: %w (%s)", err, string(out))
	}
	return nil
}
