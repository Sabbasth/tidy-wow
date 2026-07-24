package wow

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// IsRunning reports whether a World of Warcraft game process is active.
func IsRunning(ctx context.Context) (bool, error) {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "comm=").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return false, errors.New("cannot inspect running processes: ps not found")
		}
		return false, fmt.Errorf("inspect running processes: %w", err)
	}
	return isRunningProcessList(string(output)), nil
}

func isRunningProcessList(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		name := strings.ToLower(strings.TrimSpace(line))
		if name == "" {
			continue
		}
		base := name
		if index := strings.LastIndexByte(base, '/'); index >= 0 {
			base = base[index+1:]
		}
		if base == "world of warcraft" || base == "world of warcraft classic" || base == "wow" || base == "wowclassic" || base == "wowclassic.exe" || base == "wow.exe" {
			return true
		}
	}
	return false
}
