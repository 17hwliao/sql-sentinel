package kafka

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunPollingRunsImmediatelyContinuesAfterErrorAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	results := make(chan PollResult, 2)
	done := make(chan error, 1)
	go func() {
		done <- RunPolling(ctx, time.Millisecond, func(context.Context) (int, error) {
			calls++
			if calls == 2 {
				cancel()
			}
			return calls, errors.New("broker unavailable")
		}, func(result PollResult) { results <- result })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPolling error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunPolling did not stop")
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
	for i := 0; i < 2; i++ {
		if result := <-results; result.Err == nil || result.Published != i+1 {
			t.Fatalf("result=%+v", result)
		}
	}
}

func TestRunPollingRejectsInvalidInputs(t *testing.T) {
	if err := RunPolling(nil, time.Second, func(context.Context) (int, error) { return 0, nil }, nil); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := RunPolling(context.Background(), 0, func(context.Context) (int, error) { return 0, nil }, nil); err == nil {
		t.Fatal("zero interval accepted")
	}
	if err := RunPolling(context.Background(), time.Second, nil, nil); err == nil {
		t.Fatal("nil function accepted")
	}
}
