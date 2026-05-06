package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WorkerInfo describes a running agent worker. Persisted as JSON alongside
// a PID file under ~/.agentsworkhub/workers/<id>/.
type WorkerInfo struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Engine    string    `json:"engine"`
	Model     string    `json:"model,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	SkillFile string    `json:"skill_file,omitempty"`
	WorkDir   string    `json:"work_dir,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// WorkerState manages the on-disk state for a single agent worker.
type WorkerState struct {
	dir string // e.g. ~/.agentsworkhub/workers/<id>
	id  string
}

func workersBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agentsworkhub", "workers"), nil
}

// NewWorkerState creates a WorkerState for the given worker id.
func NewWorkerState(id string) (*WorkerState, error) {
	base, err := workersBaseDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &WorkerState{dir: dir, id: id}, nil
}

func (w *WorkerState) pidPath() string  { return filepath.Join(w.dir, "worker.pid") }
func (w *WorkerState) infoPath() string { return filepath.Join(w.dir, "worker.json") }
func (w *WorkerState) logPath() string  { return filepath.Join(w.dir, "worker.log") }

// LogPath returns the absolute path to the worker log file.
func (w *WorkerState) LogPath() string { return w.logPath() }

// Dir returns the worker state directory.
func (w *WorkerState) Dir() string { return w.dir }

func (w *WorkerState) WritePID(pid int) error {
	return os.WriteFile(w.pidPath(), []byte(strconv.Itoa(pid)), 0600)
}

func (w *WorkerState) ReadPID() (int, error) {
	data, err := os.ReadFile(w.pidPath())
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

func (w *WorkerState) ClearPID() error {
	err := os.Remove(w.pidPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// IsRunning checks if the worker process is still alive.
func (w *WorkerState) IsRunning() (bool, int, error) {
	pid, err := w.ReadPID()
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

// WriteInfo persists the worker metadata.
func (w *WorkerState) WriteInfo(info *WorkerInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(w.infoPath(), data, 0600)
}

// ReadInfo loads the worker metadata from disk.
func (w *WorkerState) ReadInfo() (*WorkerInfo, error) {
	data, err := os.ReadFile(w.infoPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var info WorkerInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// OpenLog opens the worker log in append mode.
func (w *WorkerState) OpenLog() (*os.File, error) {
	return os.OpenFile(w.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

// Cleanup removes the worker's state directory.
func (w *WorkerState) Cleanup() error {
	return os.RemoveAll(w.dir)
}

// ListWorkers returns WorkerState entries for all known workers.
func ListWorkers() ([]*WorkerState, error) {
	base, err := workersBaseDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var workers []*WorkerState
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		workers = append(workers, &WorkerState{dir: filepath.Join(base, e.Name()), id: e.Name()})
	}
	return workers, nil
}

// GenerateWorkerID creates a short, collision-resistant id for a worker.
// Format: "w<8 hex chars>"  — 4 bytes of crypto-quality randomness, plenty
// for human-paced agent traffic. The previous millisecond-modulo scheme had
// a real collision risk between scheduler SSE triggers and the fallback
// ticker firing in the same ms.
func GenerateWorkerID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to timestamp on the (effectively impossible) entropy
		// failure so we don't return an empty id.
		return fmt.Sprintf("w%x", time.Now().UnixNano())
	}
	return "w" + hex.EncodeToString(b[:])
}
