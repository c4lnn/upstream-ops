package scheduler

import "testing"

func TestTaskRunCoordinatorIsolatesTaskTypesAndReleasesLeases(t *testing.T) {
	coordinator := NewTaskRunCoordinator()

	balanceRun, acquired := coordinator.TryAcquire(TaskBalance)
	if !acquired {
		t.Fatal("first balance run was not acquired")
	}
	if balanceRun.StartedAt().IsZero() {
		t.Fatal("balance run start time is empty")
	}
	if _, acquired := coordinator.TryAcquire(TaskBalance); acquired {
		t.Fatal("overlapping balance run was acquired")
	}
	if _, active := coordinator.ActiveSince(TaskBalance); !active {
		t.Fatal("balance run is not marked active")
	}

	rateRun, acquired := coordinator.TryAcquire(TaskRates)
	if !acquired {
		t.Fatal("rate run should not be blocked by balance run")
	}
	rateRun.Release()

	balanceRun.Release()
	balanceRun.Release()
	if _, active := coordinator.ActiveSince(TaskBalance); active {
		t.Fatal("released balance run is still active")
	}
	if nextRun, acquired := coordinator.TryAcquire(TaskBalance); !acquired {
		t.Fatal("balance run was not available after release")
	} else {
		nextRun.Release()
	}
}

func TestTaskRunCoordinatorZeroValueIsUsable(t *testing.T) {
	var coordinator TaskRunCoordinator
	run, acquired := coordinator.TryAcquire(TaskRetention)
	if !acquired {
		t.Fatal("zero-value coordinator did not acquire retention")
	}
	run.Release()
}
