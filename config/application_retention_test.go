package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestApplicationRetentionAllowsConfigurableAge(t *testing.T) {
	for _, duration := range []string{"24h", "336h", "720h"} {
		var retention ApplicationRetentionConfig
		if err := json.Unmarshal([]byte(`{"stale_after":"`+duration+`"}`), &retention); err != nil {
			t.Fatal(err)
		}
		payload := retention.Payload("primary")
		want, _ := time.ParseDuration(duration)
		if err := payload.Validate(); err != nil || payload.StaleAfter.Value() != want || !payload.RemoveRejected {
			t.Fatalf("payload=%v err=%v", payload, err)
		}
	}
	var retention ApplicationRetentionConfig
	if err := json.Unmarshal([]byte(`{"stale_after":"0s","remove_rejected":false}`), &retention); err != nil {
		t.Fatal(err)
	}
	if retention.Payload("primary").Validate() == nil || retention.Payload("primary").RemoveRejected {
		t.Fatal("accepted zero age or ignored rejection switch")
	}
}
