package app

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

func TestDeviceCreationRetrySurvivesRestartAndRejectsChangedDraft(t *testing.T) {
	s, fs := testService(t)
	d := macDevice("")
	d.Name = "Retry Mac"
	password := "private-fixture-password"
	first, err := s.CreateDevice(d, &password, false, "creation_retry")
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := os.ReadFile(fs.Path("secrets/credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second, err := reopened.CreateDevice(d, &password, false, "creation_retry")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Revision != 1 || !second.PasswordConfigured || len(reopened.Devices().Devices) != 1 {
		t.Fatal("retry created or changed device")
	}
	after, _ := os.ReadFile(fs.Path("secrets/credentials.json"))
	if !bytes.Equal(credentials, after) {
		t.Fatal("retry rewrote credentials")
	}
	changed := d
	changed.Name = "changed after lost response"
	if _, err = reopened.CreateDevice(changed, &password, false, "creation_retry"); code(err) != "idempotency_conflict" {
		t.Fatal(err)
	}
	otherPassword := "another-password"
	if _, err = reopened.CreateDevice(d, &otherPassword, false, "creation_retry"); code(err) != "idempotency_conflict" {
		t.Fatal(err)
	}
	if len(reopened.Devices().Devices) != 1 {
		t.Fatal("conflicting retry created duplicate")
	}
	if _, err = reopened.CreateDevice(d, &password, false, "bad/key"); code(err) != "invalid_idempotency_key" {
		t.Fatal(err)
	}
	if _, err = reopened.CreateDevice(macDevice("explicit_id"), &password, false, "creation_retry"); code(err) != "idempotency_id_conflict" {
		t.Fatal(err)
	}
}

func TestConcurrentDeviceCreationRetriesPersistOnce(t *testing.T) {
	s, _ := testService(t)
	d := macDevice("")
	d.Name = "Concurrent Mac"
	password := "fixture-password"
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	failures := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			saved, err := s.CreateDevice(d, &password, false, "concurrent_create")
			if err != nil {
				failures <- err
			} else {
				ids <- saved.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var id string
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("retry IDs differ")
		}
		id = got
	}
	if len(s.Devices().Devices) != 1 || s.Devices().Revision != 2 {
		t.Fatal("creation was persisted more than once")
	}
	if !model.ValidID(id) {
		t.Fatal("generated identity invalid")
	}
}
