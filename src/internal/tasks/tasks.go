// Package tasks persists and runs the server's background work.
package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Kind string

const (
	KindJob     Kind = "job"
	KindService Kind = "service"

	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
	StatusStopped     = "stopped"
)

type Task struct {
	Key      string
	Name     string
	Kind     Kind
	Runnable bool
	NextRun  string
}

type Run struct {
	ID         int64
	TaskKey    string
	TaskName   string
	Kind       Kind
	Status     string
	QueuedAt   time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
	Error      string
}

type Summary struct {
	Task
	Status       string
	LastStarted  time.Time
	LastFinished time.Time
	LastDuration time.Duration
	LastError    string
	NextRunAt    time.Time
}

type Store struct{ sql *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open task store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS task_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_key TEXT NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		queued_at INTEGER NOT NULL,
		started_at INTEGER,
		finished_at INTEGER,
		error_text TEXT NOT NULL DEFAULT ''
	); CREATE INDEX IF NOT EXISTS idx_task_runs_queue ON task_runs(status, queued_at); CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_key, id DESC)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create task schema: %w", err)
	}
	return &Store{sql: db}, nil
}

func (s *Store) Close() error { return s.sql.Close() }

func (s *Store) RecoverInterrupted() error {
	_, err := s.sql.Exec(`UPDATE task_runs SET status = ?, finished_at = ? WHERE status IN (?, ?)`, StatusInterrupted, time.Now().UnixMilli(), StatusQueued, StatusRunning)
	return err
}

func (s *Store) enqueue(task Task) (Run, bool, error) {
	tx, err := s.sql.Begin()
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id FROM task_runs WHERE task_key = ? AND status IN (?, ?) ORDER BY id DESC LIMIT 1`, task.Key, StatusQueued, StatusRunning).Scan(&id)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Run{}, false, err
		}
		run, err := s.run(id, task.Name)
		return run, false, err
	}
	if err != sql.ErrNoRows {
		return Run{}, false, err
	}
	now := time.Now()
	result, err := tx.Exec(`INSERT INTO task_runs (task_key, kind, status, queued_at) VALUES (?, ?, ?, ?)`, task.Key, task.Kind, StatusQueued, now.UnixMilli())
	if err != nil {
		return Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	id, _ = result.LastInsertId()
	return Run{ID: id, TaskKey: task.Key, TaskName: task.Name, Kind: task.Kind, Status: StatusQueued, QueuedAt: now}, true, nil
}

func (s *Store) startService(task Task) (Run, error) {
	now := time.Now()
	result, err := s.sql.Exec(`INSERT INTO task_runs (task_key, kind, status, queued_at, started_at) VALUES (?, ?, ?, ?, ?)`, task.Key, task.Kind, StatusRunning, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Run{}, err
	}
	id, _ := result.LastInsertId()
	return Run{ID: id, TaskKey: task.Key, TaskName: task.Name, Kind: task.Kind, Status: StatusRunning, QueuedAt: now, StartedAt: now}, nil
}

func (s *Store) claimNext(names map[string]string) (Run, bool, error) {
	var id int64
	var key string
	err := s.sql.QueryRow(`SELECT id, task_key FROM task_runs WHERE status = ? ORDER BY queued_at, id LIMIT 1`, StatusQueued).Scan(&id, &key)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	now := time.Now()
	result, err := s.sql.Exec(`UPDATE task_runs SET status = ?, started_at = ? WHERE id = ? AND status = ?`, StatusRunning, now.UnixMilli(), id, StatusQueued)
	if err != nil {
		return Run{}, false, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return Run{}, false, nil
	}
	return Run{ID: id, TaskKey: key, TaskName: names[key], Kind: KindJob, Status: StatusRunning, QueuedAt: now, StartedAt: now}, true, nil
}

func (s *Store) finish(id int64, status, errorText string) error {
	if len(errorText) > 1000 {
		errorText = errorText[:1000]
	}
	var err error
	for range 3 {
		_, err = s.sql.Exec(`UPDATE task_runs SET status = ?, finished_at = ?, error_text = ? WHERE id = ?`, status, time.Now().UnixMilli(), errorText, id)
		if err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func (s *Store) run(id int64, name string) (Run, error) {
	var run Run
	var queued, started, finished sql.NullInt64
	err := s.sql.QueryRow(`SELECT id, task_key, kind, status, queued_at, started_at, finished_at, error_text FROM task_runs WHERE id = ?`, id).Scan(&run.ID, &run.TaskKey, &run.Kind, &run.Status, &queued, &started, &finished, &run.Error)
	if err != nil {
		return Run{}, err
	}
	run.TaskName = name
	run.QueuedAt = unixTime(queued)
	run.StartedAt = unixTime(started)
	run.FinishedAt = unixTime(finished)
	if !run.StartedAt.IsZero() && !run.FinishedAt.IsZero() {
		run.Duration = run.FinishedAt.Sub(run.StartedAt)
	}
	return run, nil
}

func (s *Store) summaries(definitions []Task) ([]Summary, error) {
	summaries := make([]Summary, 0, len(definitions))
	for _, task := range definitions {
		summary := Summary{Task: task, Status: "idle"}
		var started, finished sql.NullInt64
		err := s.sql.QueryRow(`SELECT status, started_at, finished_at, error_text FROM task_runs WHERE task_key = ? ORDER BY id DESC LIMIT 1`, task.Key).Scan(&summary.Status, &started, &finished, &summary.LastError)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		summary.LastStarted = unixTime(started)
		summary.LastFinished = unixTime(finished)
		if !summary.LastStarted.IsZero() && !summary.LastFinished.IsZero() {
			summary.LastDuration = summary.LastFinished.Sub(summary.LastStarted)
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *Store) history(names map[string]string, limit int) ([]Run, error) {
	rows, err := s.sql.Query(`SELECT id, task_key, kind, status, queued_at, started_at, finished_at, error_text FROM task_runs WHERE kind = ? ORDER BY id DESC LIMIT ?`, KindJob, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var run Run
		var queued, started, finished sql.NullInt64
		if err := rows.Scan(&run.ID, &run.TaskKey, &run.Kind, &run.Status, &queued, &started, &finished, &run.Error); err != nil {
			return nil, err
		}
		run.TaskName = names[run.TaskKey]
		run.QueuedAt, run.StartedAt, run.FinishedAt = unixTime(queued), unixTime(started), unixTime(finished)
		if !run.StartedAt.IsZero() && !run.FinishedAt.IsZero() {
			run.Duration = run.FinishedAt.Sub(run.StartedAt)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func unixTime(value sql.NullInt64) time.Time {
	if !value.Valid || value.Int64 == 0 {
		return time.Time{}
	}
	// Task rows before duration tracking used Unix seconds. Keep those rows
	// readable while new rows use milliseconds for short task durations.
	if value.Int64 < 100_000_000_000 {
		return time.Unix(value.Int64, 0)
	}
	return time.UnixMilli(value.Int64)
}

type Manager struct {
	store    *Store
	mu       sync.RWMutex
	tasks    map[string]Task
	order    []string
	handlers map[string]func(context.Context) error
	wake     chan struct{}
	done     chan struct{}
}

func New(store *Store) *Manager {
	return &Manager{store: store, tasks: make(map[string]Task), handlers: make(map[string]func(context.Context) error), wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (m *Manager) Register(task Task, handler func(context.Context) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tasks[task.Key]; !exists {
		m.order = append(m.order, task.Key)
	}
	m.tasks[task.Key] = task
	if handler != nil {
		m.handlers[task.Key] = handler
	}
}

func (m *Manager) Start(ctx context.Context) error {
	if err := m.store.RecoverInterrupted(); err != nil {
		return err
	}
	go func() {
		defer close(m.done)
		m.work(ctx)
	}()
	return nil
}

func (m *Manager) Wait() { <-m.done }

func (m *Manager) Enqueue(key string) (Run, bool, error) {
	m.mu.RLock()
	task, ok := m.tasks[key]
	m.mu.RUnlock()
	if !ok || !task.Runnable {
		return Run{}, false, fmt.Errorf("task cannot be run")
	}
	run, created, err := m.store.enqueue(task)
	if created {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	return run, created, err
}

// Run executes a registered job immediately while recording it like any queued run.
func (m *Manager) Run(ctx context.Context, key string) error {
	m.mu.RLock()
	task, ok := m.tasks[key]
	handler := m.handlers[key]
	m.mu.RUnlock()
	if !ok || task.Kind != KindJob || handler == nil {
		return fmt.Errorf("task cannot be run")
	}
	run, err := m.store.startService(task)
	if err != nil {
		return err
	}
	err = handler(ctx)
	status, message := StatusSucceeded, ""
	if err != nil {
		status, message = StatusFailed, err.Error()
		log.Printf("task %s (run %d) failed: %v", key, run.ID, err)
	}
	if finishErr := m.store.finish(run.ID, status, message); finishErr != nil {
		if err != nil {
			return fmt.Errorf("task failed: %w; record completion: %v", err, finishErr)
		}
		return fmt.Errorf("record task completion: %w", finishErr)
	}
	return err
}

func (m *Manager) StartService(ctx context.Context, key string, fn func(context.Context)) error {
	m.mu.RLock()
	task, ok := m.tasks[key]
	m.mu.RUnlock()
	if !ok || task.Kind != KindService {
		return fmt.Errorf("unknown service %q", key)
	}
	run, err := m.store.startService(task)
	if err != nil {
		return err
	}
	go func() {
		fn(ctx)
		status := StatusStopped
		if ctx.Err() == nil {
			status = StatusFailed
		}
		if err := m.store.finish(run.ID, status, ""); err != nil {
			log.Printf("task service %s: record completion: %v", key, err)
		}
	}()
	return nil
}

func (m *Manager) Summaries() ([]Summary, error)    { return m.store.summaries(m.definitions()) }
func (m *Manager) History(limit int) ([]Run, error) { return m.store.history(m.names(), limit) }

func (m *Manager) definitions() []Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Task, 0, len(m.tasks))
	for _, key := range m.order {
		result = append(result, m.tasks[key])
	}
	return result
}
func (m *Manager) names() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.tasks))
	for key, task := range m.tasks {
		result[key] = task.Name
	}
	return result
}
func (m *Manager) work(ctx context.Context) {
	for {
		run, ok, err := m.store.claimNext(m.names())
		if err == nil && ok {
			m.mu.RLock()
			handler := m.handlers[run.TaskKey]
			m.mu.RUnlock()
			if handler == nil {
				_ = m.store.finish(run.ID, StatusFailed, "no task handler registered")
				continue
			}
			err = handler(ctx)
			status, message := StatusSucceeded, ""
			if err != nil {
				status, message = StatusFailed, err.Error()
				log.Printf("task %s (run %d) failed: %v", run.TaskKey, run.ID, err)
			}
			if finishErr := m.store.finish(run.ID, status, message); finishErr != nil {
				log.Printf("task %s: record completion: %v", run.TaskKey, finishErr)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-time.After(time.Second):
		}
	}
}
