package cmd

import "os"

// processStillAlive returns true if the process with the given PID is running.
func processStillAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return checkProcessAlive(proc)
}
