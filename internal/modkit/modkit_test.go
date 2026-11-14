package modkit

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWebhookSignature(t *testing.T) {
	now := time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)
	body := []byte(`{"id":"e1"}`)
	h := SignWebhook("s3", now, body)
	if err := VerifyWebhook("s3", h, body, now.Add(time.Minute), 300); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"wrong secret": VerifyWebhook("other", h, body, now, 300),
		"changed body": VerifyWebhook("s3", h, []byte(`{"id":"e2"}`), now, 300),
		"too old":      VerifyWebhook("s3", h, body, now.Add(6*time.Minute), 300),
		"too new":      VerifyWebhook("s3", h, body, now.Add(-6*time.Minute), 300),
		"no secret":    VerifyWebhook("", h, body, now, 300),
		"malformed":    VerifyWebhook("s3", "v1=abc", body, now, 300),
	} {
		if err == nil {
			t.Errorf("%s passed", name)
		}
	}
}

func TestNewID(t *testing.T) {
	ms := int64(1763140000123)
	id := NewID(ms)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("not a UUIDv7: %s", id)
	}
	if got := strings.ReplaceAll(id, "-", "")[:12]; got != fmt.Sprintf("%012x", ms) {
		t.Fatalf("time field %s, want %012x", got, ms)
	}
	if NewID(ms) == id {
		t.Fatal("two ids for the same millisecond are equal")
	}
	if len(RandomHex()) != 32 {
		t.Fatal("RandomHex is not 16 bytes")
	}
}
