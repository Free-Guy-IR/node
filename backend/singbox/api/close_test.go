package api

import (
	"context"
	"sync"
	"testing"
	"time"
)

func callWithoutPanic(t *testing.T, name string, call func() error) error {
	t.Helper()

	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s on a closed client panicked: %v", name, r)
			}
		}()
		err = call()
	}()
	return err
}

func TestAStatsCallOnAClosedClientFailsInsteadOfPanicking(t *testing.T) {
	_, port := startFakeStatsServer(t)
	client, err := NewClient(port)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := callWithoutPanic(t, "GetSysStats", func() error {
		_, err := client.GetSysStats(ctx)
		return err
	}); err == nil {
		t.Fatal("GetSysStats on a closed client must fail")
	}
	if err := callWithoutPanic(t, "QueryStats", func() error {
		_, err := client.QueryStats(ctx, "user>>>", false)
		return err
	}); err == nil {
		t.Fatal("QueryStats on a closed client must fail")
	}
	if err := callWithoutPanic(t, "DrainInboundCounters", func() error {
		_, err := client.DrainInboundCounters(ctx)
		return err
	}); err == nil {
		t.Fatal("DrainInboundCounters on a closed client must fail")
	}
}

func TestClosingTheClientDoesNotRaceAStatsCallInFlight(t *testing.T) {
	_, port := startFakeStatsServer(t)

	for range 50 {
		client, err := NewClient(port)
		if err != nil {
			t.Fatal(err)
		}

		started := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("a stats call racing Close panicked: %v", r)
				}
			}()
			close(started)
			for range 20 {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, _ = client.GetSysStats(ctx)
				cancel()
			}
		}()

		<-started
		client.Close()
		wg.Wait()
	}
}
