// Package modkit holds the module's side of the core's wire protocol: the context token
// header and the webhook signature format the core sends (HMAC-SHA256 of "<t>.<body>").
//
// Packaging and signing are not here: cmd/documents uses the core's public kit,
// pkg/modulekit, so the package is checked and signed exactly as the core verifies it.
package modkit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ContextHeader carries a frame's context token on a module's calls back to the core.
const ContextHeader = "X-Togavsu-Context-Token"

// SignatureHeader carries the webhook signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of
// "<t>.<body>" under the module's webhook secret>.
const SignatureHeader = "X-Togavsu-Signature"

// EventPayload is what a module receives for one event.
type EventPayload struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	RecordType string `json:"record_type"`
	RecordID   string `json:"record_id"`
	MatterID   string `json:"matter_id,omitempty"`
	OccurredAt string `json:"occurred_at"`
	Module     string `json:"module"`
}

// SignWebhook returns the signature header value for body (tests and the fake core use
// it to send events the way the core does).
func SignWebhook(secret string, at time.Time, body []byte) string {
	t := strconv.Itoa(int(at.Unix()))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t + "."))
	mac.Write(body)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhook checks a signature header against body, with its timestamp within
// toleranceSeconds of now.
func VerifyWebhook(secret, header string, body []byte, now time.Time, toleranceSeconds int64) error {
	var t, v1 string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			t = v
		case "v1":
			v1 = v
		}
	}
	sec, err := strconv.ParseInt(t, 10, 64)
	if err != nil || v1 == "" || secret == "" {
		return errors.New("malformed webhook signature")
	}
	if d := now.Unix() - sec; d > toleranceSeconds || d < -toleranceSeconds {
		return errors.New("webhook signature is outside the allowed clock skew")
	}
	want := SignWebhook(secret, time.Unix(sec, 0), body)
	if !hmac.Equal([]byte(want), []byte("t="+t+",v1="+v1)) {
		return errors.New("webhook signature doesn't match")
	}
	return nil
}
