package pgexec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingObserver struct {
	mu     sync.Mutex
	events []QueryEvent
}

func (r *recordingObserver) ObserveQuery(_ context.Context, ev QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func TestRunTxObservesEveryAttempt(t *testing.T) {
	obs := &recordingObserver{}
	attempts := 0
	err := RunTx(context.Background(), &fakeDB{}, TxOptions{MaxRetries: 2, Log: quiet, Observer: obs}, Identity{}, func(Querier) error {
		attempts++
		if attempts < 3 {
			return serFail()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.events) != 3 {
		t.Fatalf("events = %d, want 3", len(obs.events))
	}
	for i, ev := range obs.events {
		if ev.Op != OpRunTx || ev.Attempt != i {
			t.Errorf("event %d = %+v, want op %s attempt %d", i, ev, OpRunTx, i)
		}
		if ev.Duration < 0 {
			t.Errorf("event %d: negative duration", i)
		}
	}
	if !IsSerializationFailure(obs.events[0].Err) || !IsSerializationFailure(obs.events[1].Err) || obs.events[2].Err != nil {
		t.Fatalf("errors = %v, %v, %v", obs.events[0].Err, obs.events[1].Err, obs.events[2].Err)
	}
}

func TestRunTxObservesBeginError(t *testing.T) {
	obs := &recordingObserver{}
	beginErr := errors.New("no connection")
	err := RunTx(context.Background(), &fakeDB{beginErr: beginErr}, TxOptions{Observer: obs}, Identity{}, func(Querier) error { return nil })
	if !errors.Is(err, beginErr) {
		t.Fatalf("err = %v", err)
	}
	if len(obs.events) != 1 || !errors.Is(obs.events[0].Err, beginErr) {
		t.Fatalf("events = %+v", obs.events)
	}
}

func TestQueryTxDoesNotObserveRejectedBatch(t *testing.T) {
	obs := &recordingObserver{}
	err := QueryTx(context.Background(), nil, TxOptions{Observer: obs}, Identity{}, Stmt{SQL: "COMMIT"})
	if err == nil {
		t.Fatal("want transaction-control rejection")
	}
	if len(obs.events) != 0 {
		t.Fatalf("events = %+v, want none before anything is sent", obs.events)
	}
}

func TestObserve(t *testing.T) {
	Observe(context.Background(), nil, "query", time.Now(), nil)
	obs := &recordingObserver{}
	want := errors.New("boom")
	Observe(context.Background(), obs, "query", time.Now().Add(-time.Second), want)
	if len(obs.events) != 1 {
		t.Fatalf("events = %d", len(obs.events))
	}
	ev := obs.events[0]
	if ev.Op != "query" || ev.Attempt != 0 || !errors.Is(ev.Err, want) || ev.Duration < time.Second {
		t.Fatalf("event = %+v", ev)
	}
}
