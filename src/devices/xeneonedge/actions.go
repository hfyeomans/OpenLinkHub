package xeneonedge

// Launch App actions for the XENEON EDGE touchscreen.
//
// Actions are user-defined launchers stored in database/xeneon/actions.json.
// Execution is deliberately restrictive: no shell, argv slices only,
// absolute executable paths, arg-count and byte limits, per-action timeout,
// single-flight and a short debounce so a touchscreen double-tap cannot
// spawn duplicates.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"OpenLinkHub/src/common"
	"OpenLinkHub/src/logger"
)

const (
	actionTimeoutSeconds = 10
	actionDebounceMs     = 1000
	actionMaxArgs        = 8
	actionArgMaxBytes    = 256
	actionPathMaxBytes   = 256
	actionIDMaxLen       = 32
	actionNameMaxLen     = 64
	actionIconMaxLen     = 16
	actionMaxCount       = 32
)

// Action describes a single launcher exposed on the Edge touchscreen.
type Action struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Icon    string   `json:"icon"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type actionState struct {
	mu      sync.Mutex
	lastRun time.Time
	running bool
}

var (
	actionsMutex  sync.Mutex
	actionsLoaded bool
	actionsCache  []Action
	actionsFlight sync.Mutex
	actionsStates = make(map[string]*actionState)
)

func actionsFile() string {
	return pwd + "/database/xeneon/actions.json"
}

// validateAction checks a single action definition against the allowlist rules.
func validateAction(a Action) error {
	if !common.AlphanumericDashRegex.MatchString(a.ID) || len(a.ID) > actionIDMaxLen {
		return fmt.Errorf("invalid id %q: letters, numbers and dashes only", a.ID)
	}
	if a.Name == "" || len(a.Name) > actionNameMaxLen {
		return fmt.Errorf("invalid name for id %q", a.ID)
	}
	if len(a.Icon) > actionIconMaxLen {
		return fmt.Errorf("icon too long for id %q", a.ID)
	}
	if len(a.Command) == 0 || len(a.Command) > actionPathMaxBytes {
		return fmt.Errorf("invalid command length for id %q", a.ID)
	}
	if !filepath.IsAbs(a.Command) {
		return fmt.Errorf("command for id %q must be an absolute path", a.ID)
	}
	clean := filepath.Clean(a.Command)
	if clean != a.Command {
		return fmt.Errorf("command path for id %q must be canonical", a.ID)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("command for id %q not found: %w", a.ID, err)
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return fmt.Errorf("command for id %q is not an executable file", a.ID)
	}
	if len(a.Args) > actionMaxArgs {
		return fmt.Errorf("too many args for id %q (max %d)", a.ID, actionMaxArgs)
	}
	for _, arg := range a.Args {
		if len(arg) > actionArgMaxBytes {
			return fmt.Errorf("arg too long for id %q (max %d bytes)", a.ID, actionArgMaxBytes)
		}
	}
	return nil
}

// loadActions reads and validates database/xeneon/actions.json once.
// A missing file is not an error: the feature is simply unconfigured.
func loadActions() []Action {
	actionsMutex.Lock()
	defer actionsMutex.Unlock()
	if actionsLoaded {
		return actionsCache
	}
	actionsLoaded = true
	actionsCache = []Action{}

	file, err := os.Open(actionsFile())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Log(logger.Fields{"error": err, "location": actionsFile()}).Warn("Unable to open actions file")
		}
		return actionsCache
	}
	defer func() {
		_ = file.Close()
	}()

	var list []Action
	if err := json.NewDecoder(file).Decode(&list); err != nil {
		logger.Log(logger.Fields{"error": err, "location": actionsFile()}).Warn("Unable to decode actions file")
		return actionsCache
	}

	seen := make(map[string]bool, len(list))
	for _, a := range list {
		if err := validateAction(a); err != nil {
			logger.Log(logger.Fields{"action": a.ID, "error": err}).Warn("Skipping invalid action definition")
			continue
		}
		if seen[a.ID] {
			logger.Log(logger.Fields{"action": a.ID}).Warn("Skipping duplicate action id")
			continue
		}
		seen[a.ID] = true
		actionsCache = append(actionsCache, a)
	}
	return actionsCache
}

// Actions returns the configured actions for template rendering.
// Exported on Device so xeneon-actions.html can render buttons server-side.
func (d *Device) Actions() []Action {
	return ListActions()
}

// ListActions returns the configured actions (package-level, for the API).
func ListActions() []Action {
	list := loadActions()
	out := make([]Action, len(list))
	copy(out, list)
	return out
}

// SaveActions validates and persists the full actions list, replacing the file.
func SaveActions(list []Action) error {
	if len(list) > actionMaxCount {
		return fmt.Errorf("too many actions (max %d)", actionMaxCount)
	}
	seen := make(map[string]bool, len(list))
	for i := range list {
		if err := validateAction(list[i]); err != nil {
			return fmt.Errorf("action %d: %w", i, err)
		}
		if seen[list[i].ID] {
			return fmt.Errorf("duplicate action id %q", list[i].ID)
		}
		seen[list[i].ID] = true
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: a crash mid-write must not corrupt the store.
	path := actionsFile()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	actionsMutex.Lock()
	actionsLoaded = true
	actionsCache = make([]Action, len(list))
	copy(actionsCache, list)
	actionsMutex.Unlock()
	bumpConfigRevision() // buttons are server-rendered; the kiosk must reload
	return nil
}

// RunAction executes one configured action with timeout, single-flight and
// debounce. Returns 1 on success, 0 unknown id, 2 already running,
// 3 debounced, 4 execution failure.
func RunAction(id string) uint8 {
	list := loadActions()
	var action *Action
	for i := range list {
		if list[i].ID == id {
			action = &list[i]
			break
		}
	}
	if action == nil {
		return 0
	}
	if err := validateAction(*action); err != nil {
		// Re-validate at exec time: the file may have changed since the
		// cache was populated.
		logger.Log(logger.Fields{"action": id, "error": err}).Warn("Action no longer valid")
		return 0
	}

	actionsFlight.Lock()
	state, ok := actionsStates[id]
	if !ok {
		state = &actionState{}
		actionsStates[id] = state
	}
	actionsFlight.Unlock()

	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		return 2
	}
	if time.Since(state.lastRun) < time.Duration(actionDebounceMs)*time.Millisecond {
		state.mu.Unlock()
		return 3
	}
	state.running = true
	state.lastRun = time.Now()
	state.mu.Unlock()

	defer func() {
		state.mu.Lock()
		state.running = false
		state.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), actionTimeoutSeconds*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, action.Command, action.Args...)
	// Kill the whole process group on timeout so wrapper/launcher
	// scripts cannot orphan children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		logger.Log(logger.Fields{"action": id, "error": err}).Warn("Action execution failed")
		return 4
	}
	logger.Log(logger.Fields{"action": id}).Info("Action executed")
	return 1
}
