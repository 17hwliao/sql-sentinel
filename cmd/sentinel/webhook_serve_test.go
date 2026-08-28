package main

import "testing"

func TestValidateWebhookListenAllowsOnlyIPv4Loopback(t *testing.T) {
	if err := validateWebhookListen("127.0.0.1:8080"); err != nil {
		t.Fatalf("loopback address rejected: %v", err)
	}
	for _, addr := range []string{"0.0.0.0:8080", "localhost:8080", "[::1]:8080", "127.0.0.1"} {
		if err := validateWebhookListen(addr); err == nil {
			t.Fatalf("non-approved listen address accepted: %s", addr)
		}
	}
}
