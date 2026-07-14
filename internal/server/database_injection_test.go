package server

import (
	"sync"
	"testing"

	"fastrg-controller/internal/db"
)

func TestDatabaseLateInjectionIsConcurrentSafe(t *testing.T) {
	rest := NewRestServer(nil, nil, nil)
	manager := NewNodeMonitorManager(nil)
	if rest.Database() != nil {
		t.Fatal("REST server database should be nil before injection")
	}
	if manager.Database() != nil {
		t.Fatal("node monitor manager database should be nil before injection")
	}

	first := new(db.DB)
	second := new(db.DB)
	const iterations = 10_000
	start := make(chan struct{})
	var wg sync.WaitGroup

	wg.Go(func() {
		<-start
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				rest.SetDatabase(first)
				manager.SetDatabase(first)
			} else {
				rest.SetDatabase(second)
				manager.SetDatabase(second)
			}
		}
	})
	for range 8 {
		wg.Go(func() {
			<-start
			for i := 0; i < iterations; i++ {
				assertKnownDatabase(t, rest.Database(), first, second)
				assertKnownDatabase(t, manager.Database(), first, second)
			}
		})
	}
	close(start)
	wg.Wait()

	rest.SetDatabase(first)
	manager.SetDatabase(first)
	if rest.Database() != first {
		t.Fatal("REST server did not expose the injected database")
	}
	if manager.Database() != first {
		t.Fatal("node monitor manager did not expose the injected database")
	}
}

func assertKnownDatabase(t *testing.T, got, first, second *db.DB) {
	t.Helper()
	if got != nil && got != first && got != second {
		t.Errorf("database getter returned unexpected pointer %p", got)
	}
}
