package main

import "testing"

func TestWebhookNotifiesMedia(t *testing.T) {
	for _, tc := range []struct {
		name, setting, mediaType string
		want                     bool
	}{
		{"off by default audio", "", "audio", false},
		{"off by default document", "", "document", false},
		{"enabled audio", "true", "audio", true},
		{"enabled video", "true", "video", true},
		{"enabled document", "true", "document", true},
		{"enabled image stays on its own path", "true", "image", false},
		{"enabled text message", "true", "", false},
		{"explicitly disabled audio", "false", "audio", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WEBHOOK_NOTIFY_MEDIA", tc.setting)
			if got := webhookNotifiesMedia(tc.mediaType); got != tc.want {
				t.Fatalf("webhookNotifiesMedia(%q) = %v, want %v", tc.mediaType, got, tc.want)
			}
		})
	}
}
