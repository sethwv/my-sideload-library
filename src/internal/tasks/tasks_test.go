package tasks

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return New(store)
}

func TestManagerRunsQueuedJobOnce(t *testing.T) {
	manager := newTestManager(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	manager.Register(Task{Key: "scan", Name: "Scan", Kind: KindJob, Runnable: true}, func(context.Context) error {
		started <- struct{}{}
		<-release
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		manager.Wait()
	})
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, created, err := manager.Enqueue("scan"); err != nil || !created {
		t.Fatalf("first Enqueue() = created %t, err %v", created, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	if _, created, err := manager.Enqueue("scan"); err != nil || created {
		t.Fatalf("duplicate Enqueue() = created %t, err %v", created, err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		history, err := manager.History(10)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) == 1 && history[0].Status == StatusSucceeded {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("queued job did not complete")
}

func TestManagerDisablesJob(t *testing.T) {
	manager := newTestManager(t)
	manager.Register(Task{Key: "digest", Name: "Digest", Kind: KindJob, Runnable: true}, func(context.Context) error {
		t.Fatal("disabled task ran")
		return nil
	})
	manager.SetEnabled("digest", false)
	if _, _, err := manager.Enqueue("digest"); err == nil {
		t.Fatal("Enqueue succeeded for disabled task")
	}
	if err := manager.Run(context.Background(), "digest"); err == nil {
		t.Fatal("Run succeeded for disabled task")
	}
}

func TestManagerRecoversInterruptedRuns(t *testing.T) {
	manager := newTestManager(t)
	manager.Register(Task{Key: "scan", Name: "Scan", Kind: KindJob, Runnable: true}, func(context.Context) error { return nil })
	if _, created, err := manager.Enqueue("scan"); err != nil || !created {
		t.Fatalf("Enqueue() = created %t, err %v", created, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	manager.Wait()
	history, err := manager.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Status != StatusInterrupted {
		t.Fatalf("history = %#v, want interrupted run", history)
	}
}

func TestHistoryExcludesServices(t *testing.T) {
	manager := newTestManager(t)
	manager.Register(Task{Key: "scan", Name: "Scan", Kind: KindJob, Runnable: true}, func(context.Context) error { return nil })
	manager.Register(Task{Key: "watch", Name: "Watch", Kind: KindService}, nil)
	if err := manager.Run(context.Background(), "scan"); err != nil {
		t.Fatal(err)
	}
	if err := manager.StartService(context.Background(), "watch", func(context.Context) {}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		history, err := manager.History(10)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) == 1 && history[0].TaskKey == "scan" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("history included a service or omitted the job")
}

func TestManagerRecordsMillisecondDuration(t *testing.T) {
	manager := newTestManager(t)
	manager.Register(Task{Key: "scan", Name: "Scan", Kind: KindJob, Runnable: true}, func(context.Context) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	if err := manager.Run(context.Background(), "scan"); err != nil {
		t.Fatal(err)
	}
	history, err := manager.History(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Duration < time.Millisecond {
		t.Fatalf("history = %#v, want a millisecond duration", history)
	}
}

func TestManagerRecordsFailedJobError(t *testing.T) {
	manager := newTestManager(t)
	manager.Register(Task{Key: "scan", Name: "Scan", Kind: KindJob, Runnable: true}, func(context.Context) error {
		return fmt.Errorf("library is unavailable")
	})

	if err := manager.Run(context.Background(), "scan"); err == nil {
		t.Fatal("Run() succeeded, want failure")
	}
	history, err := manager.History(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Status != StatusFailed || history[0].Error != "library is unavailable" {
		t.Fatalf("history = %#v, want persisted failed run", history)
	}
}

func TestManagerHidesInternalTasksFromSummariesAndHistory(t *testing.T) {
	manager := newTestManager(t)
	manager.Register(Task{Key: "visible", Name: "Visible", Kind: KindJob, Runnable: true}, func(context.Context) error { return nil })
	manager.Register(Task{Key: "internal", Name: "Internal", Kind: KindJob, Hidden: true}, func(context.Context) error { return nil })
	if err := manager.Run(context.Background(), "visible"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Run(context.Background(), "internal"); err != nil {
		t.Fatal(err)
	}

	summaries, err := manager.Summaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Key != "visible" {
		t.Fatalf("summaries = %#v, want only the visible task", summaries)
	}
	history, err := manager.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].TaskKey != "visible" {
		t.Fatalf("history = %#v, want only the visible task", history)
	}
}
