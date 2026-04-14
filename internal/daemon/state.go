package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TaskStatus tracks the current work state persisted to disk.
type TaskStatus struct {
	JobID     string    `json:"job_id"`
	JobTitle  string    `json:"job_title"`
	Phase     string    `json:"phase"` // bidding|running_ai|submitting|waiting_feedback|rerunning
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// State manages pid file, log file, and current task file under configDir.
// Each patrol role has its own PID file so multiple roles can run concurrently.
type State struct {
	dir  string
	role string // "executor", "publisher", "reviewer", or "" (treated as executor)
}

func NewState(configDir string) *State {
	return &State{dir: configDir, role: "executor"}
}

func NewStateForRole(configDir, role string) *State {
	return &State{dir: configDir, role: role}
}

// AllRoleStates returns State instances for every known patrol role.
// Used by stop/status to inspect all roles at once.
func AllRoleStates(configDir string) []*State {
	return []*State{
		{dir: configDir, role: "executor"},
		{dir: configDir, role: "publisher"},
		{dir: configDir, role: "reviewer"},
	}
}

func (s *State) Role() string {
	if s.role == "" {
		return "executor"
	}
	return s.role
}

// pidPath returns the role-specific PID file path.
// executor uses patrol.pid for backward compatibility; others use patrol.<role>.pid.
func (s *State) pidPath() string {
	role := s.Role()
	if role == "executor" {
		return filepath.Join(s.dir, "patrol.pid")
	}
	return filepath.Join(s.dir, "patrol."+role+".pid")
}

// logPath is shared across all roles.
func (s *State) logPath() string { return filepath.Join(s.dir, "patrol.log") }

func (s *State) taskPath() string { return filepath.Join(s.dir, "patrol.task.json") }

// --- PID ---

func (s *State) WritePID(pid int) error {
	return os.WriteFile(s.pidPath(), []byte(strconv.Itoa(pid)), 0600)
}

func (s *State) ReadPID() (int, error) {
	data, err := os.ReadFile(s.pidPath())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid file: %w", err)
	}
	return pid, nil
}

func (s *State) ClearPID() error {
	err := os.Remove(s.pidPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// IsRunning returns true if a daemon process with the stored PID is alive.
func (s *State) IsRunning() (bool, int, error) {
	pid, err := s.ReadPID()
	if err != nil {
		return false, 0, err
	}
	if pid == 0 {
		return false, 0, nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0, nil
	}
	if err := processAlive(proc); err != nil {
		return false, pid, nil
	}
	return true, pid, nil
}

// --- Log ---

// OpenLog opens (or creates) the log file in append mode, returning a writer.
func (s *State) OpenLog() (io.WriteCloser, error) {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(s.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

// LogPath returns the absolute path to the log file.
func (s *State) LogPath() string { return s.logPath() }

// --- Current task (executor only) ---

func (s *State) WriteTask(t *TaskStatus) error {
	t.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.taskPath(), data, 0600)
}

func (s *State) ReadTask() (*TaskStatus, error) {
	data, err := os.ReadFile(s.taskPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t TaskStatus
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *State) ClearTask() error {
	err := os.Remove(s.taskPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
