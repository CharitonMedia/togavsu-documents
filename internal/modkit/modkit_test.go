package modkit

import (
	"testing"
	"time"
)

func TestWebhookSignature(t *testing.T) {
	now := time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)
	body := []byte(`{"id":"e1"}`)
	h := SignWebhook("s3", now, body)
	if err := VerifyWebhook("s3", h, body, now.Add(time.Minute), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"wrong secret": VerifyWebhook("other", h, body, now, 5*time.Minute),
		"changed body": VerifyWebhook("s3", h, []byte(`{"id":"e2"}`), now, 5*time.Minute),
		"too old":      VerifyWebhook("s3", h, body, now.Add(6*time.Minute), 5*time.Minute),
		"too new":      VerifyWebhook("s3", h, body, now.Add(-6*time.Minute), 5*time.Minute),
		"no secret":    VerifyWebhook("", h, body, now, 5*time.Minute),
		"malformed":    VerifyWebhook("s3", "v1=abc", body, now, 5*time.Minute),
	} {
		if err == nil {
			t.Errorf("%s passed", name)
		}
	}
}
