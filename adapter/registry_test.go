package adapter_test

import (
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
)

func TestRegistryRejectsDuplicateAdapter(t *testing.T) {
	r := adapter.NewRegistry()
	if err := r.Register("hh", hh.New); err != nil {
		t.Fatalf("register hh: %v", err)
	}
	if err := r.Register("hh", hh.New); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}
