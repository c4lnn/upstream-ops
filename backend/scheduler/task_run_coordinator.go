package scheduler

import (
	"sync"
	"time"
)

// TaskType identifies a scheduled task class. Task classes are isolated so a
// long-running balance scan does not prevent a rate scan or retention cleanup.
type TaskType string

const (
	TaskBalance   TaskType = "balance"
	TaskRates     TaskType = "rates"
	TaskRetention TaskType = "retention"
)

type taskRunState struct {
	startedAt time.Time
	token     uint64
}

// TaskRunCoordinator coordinates scheduled task runs within one process.
// It is deliberately independent from Scheduler so it can be shared by
// schedulers that overlap briefly during runtime configuration replacement.
type TaskRunCoordinator struct {
	mu     sync.Mutex
	runs   map[TaskType]taskRunState
	nextID uint64
}

// TaskRun is the lease returned by TryAcquire. A lease must be released only
// after the callback has actually returned; Release is idempotent.
type TaskRun struct {
	coordinator *TaskRunCoordinator
	task        TaskType
	token       uint64
	startedAt   time.Time
	once        sync.Once
}

func NewTaskRunCoordinator() *TaskRunCoordinator {
	return &TaskRunCoordinator{runs: make(map[TaskType]taskRunState)}
}

// TryAcquire attempts to start one task class without blocking. It returns
// false when that class already has an active run.
func (c *TaskRunCoordinator) TryAcquire(task TaskType) (*TaskRun, bool) {
	if c == nil {
		return &TaskRun{task: task, startedAt: time.Now()}, true
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs == nil {
		c.runs = make(map[TaskType]taskRunState)
	}
	if _, ok := c.runs[task]; ok {
		return nil, false
	}

	c.nextID++
	startedAt := time.Now()
	c.runs[task] = taskRunState{startedAt: startedAt, token: c.nextID}
	return &TaskRun{
		coordinator: c,
		task:        task,
		token:       c.nextID,
		startedAt:   startedAt,
	}, true
}

// ActiveSince returns the start time of an active task run.
func (c *TaskRunCoordinator) ActiveSince(task TaskType) (time.Time, bool) {
	if c == nil {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.runs[task]
	return state.startedAt, ok
}

// StartedAt returns when this task run acquired its lease.
func (r *TaskRun) StartedAt() time.Time {
	if r == nil {
		return time.Time{}
	}
	return r.startedAt
}

// Release marks the task class as available again. It only releases the exact
// lease that acquired the task, which makes duplicate or stale releases safe.
func (r *TaskRun) Release() {
	if r == nil || r.coordinator == nil {
		return
	}
	r.once.Do(func() {
		r.coordinator.mu.Lock()
		defer r.coordinator.mu.Unlock()
		state, ok := r.coordinator.runs[r.task]
		if ok && state.token == r.token {
			delete(r.coordinator.runs, r.task)
		}
	})
}
