package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func stubProfilePictureHooks(t *testing.T, info *types.ProfilePictureInfo, infoErr error, body []byte) *int {
	t.Helper()
	oldInfo, oldFetch := getProfilePictureInfo, fetchProfilePictureBytes
	fetches := 0
	getProfilePictureInfo = func(context.Context, *whatsmeow.Client, types.JID, bool) (*types.ProfilePictureInfo, error) {
		return info, infoErr
	}
	fetchProfilePictureBytes = func(context.Context, string) ([]byte, error) {
		fetches++
		return body, nil
	}
	t.Cleanup(func() { getProfilePictureInfo, fetchProfilePictureBytes = oldInfo, oldFetch })
	return &fetches
}

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func TestDownloadProfilePicture_SavesAndCaches(t *testing.T) {
	dir := chdirTemp(t)
	fetches := stubProfilePictureHooks(t, &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/x", ID: "12345"}, nil, []byte("jpegdata"))
	jid := types.NewJID("5585999990000", types.DefaultUserServer)

	status, resp := downloadProfilePicture(context.Background(), nil, jid, false)
	if status != http.StatusOK || !resp.Success || resp.Status != "ok" {
		t.Fatalf("unexpected result: %d %+v", status, resp)
	}
	want := filepath.Join(dir, "store", "profile-pictures", "5585999990000@s.whatsapp.net_12345.jpg")
	if resp.Path != want {
		t.Fatalf("path = %q, want %q", resp.Path, want)
	}
	if got, _ := os.ReadFile(want); string(got) != "jpegdata" {
		t.Fatalf("file content = %q", got)
	}

	// Same picture ID: served from disk, no second download.
	status, resp = downloadProfilePicture(context.Background(), nil, jid, false)
	if status != http.StatusOK || *fetches != 1 || !strings.Contains(resp.Message, "already") {
		t.Fatalf("expected cache hit, got %d fetches=%d %+v", status, *fetches, resp)
	}
}

func TestDownloadProfilePicture_PreviewUsesSeparateFile(t *testing.T) {
	chdirTemp(t)
	stubProfilePictureHooks(t, &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/x", ID: "1"}, nil, []byte("p"))
	_, resp := downloadProfilePicture(context.Background(), nil, types.NewJID("123", types.GroupServer), true)
	if !strings.HasSuffix(resp.Filename, "_1_preview.jpg") {
		t.Fatalf("filename = %q", resp.Filename)
	}
}

func TestDownloadProfilePicture_ErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		info       *types.ProfilePictureInfo
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not set", nil, whatsmeow.ErrProfilePictureNotSet, http.StatusNotFound, "no_picture"},
		{"private", nil, whatsmeow.ErrProfilePictureUnauthorized, http.StatusForbidden, "private"},
		{"rate limit", nil, whatsmeow.ErrIQRateOverLimit, http.StatusTooManyRequests, "rate_limited"},
		{"other", nil, errors.New("boom"), http.StatusBadGateway, "error"},
		{"nil info", nil, nil, http.StatusNotFound, "no_picture"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chdirTemp(t)
			stubProfilePictureHooks(t, tc.info, tc.err, nil)
			status, resp := downloadProfilePicture(context.Background(), nil, types.NewJID("1", types.DefaultUserServer), false)
			if status != tc.wantStatus || resp.Status != tc.wantCode || resp.Success {
				t.Fatalf("got %d %+v", status, resp)
			}
		})
	}
}

func TestProfilePictureStorePaths_RejectsTraversal(t *testing.T) {
	chdirTemp(t)
	jid := types.NewJID("1", types.DefaultUserServer)
	for _, id := range []string{"", "..", "../x", "a/b"} {
		if _, _, _, err := profilePictureStorePaths(jid, id, false); err == nil {
			t.Errorf("picture ID %q should be rejected", id)
		}
	}
}

func TestProfilePictureHandler_Validation(t *testing.T) {
	const token = "supersecrettoken1234567890abcdef"
	handler := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, token, nil)

	cases := []struct {
		name, path, method, body string
		want                     int
	}{
		{"wrong method", "/api/profile-picture", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"bad json", "/api/profile-picture", http.MethodPost, "{", http.StatusBadRequest},
		{"missing jid", "/api/profile-picture", http.MethodPost, "{}", http.StatusBadRequest},
		{"invalid jid", "/api/profile-picture", http.MethodPost, `{"jid":"1.2.3@s.whatsapp.net"}`, http.StatusBadRequest},
		{"not connected", "/api/profile-picture", http.MethodPost, `{"jid":"5585999990000@s.whatsapp.net"}`, http.StatusServiceUnavailable},
		{"group wrong method", "/api/group-participants", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"group missing jid", "/api/group-participants", http.MethodPost, "{}", http.StatusBadRequest},
		{"group not a group jid", "/api/group-participants", http.MethodPost, `{"group_jid":"5585999990000@s.whatsapp.net"}`, http.StatusBadRequest},
		{"group not connected", "/api/group-participants", http.MethodPost, `{"group_jid":"120363012345678901@g.us"}`, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://127.0.0.1:8080"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+token)
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tc.want {
				t.Errorf("expected %d, got %d: %s", tc.want, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestProfilePictureHandler_RequiresAuth(t *testing.T) {
	handler := newRESTMux(newTestClient(&mockLIDStore{}), newTestMessageStore(t), 8080, "supersecrettoken1234567890abcdef", nil)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/profile-picture", strings.NewReader(`{"jid":"1@s.whatsapp.net"}`))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.Code)
	}
}

func TestGroupParticipantInfos_MapsLIDAndPhone(t *testing.T) {
	lid := types.NewJID("231241139937355", types.HiddenUserServer)
	pn := types.NewJID("5585999990000", types.DefaultUserServer)
	got := groupParticipantInfos([]types.GroupParticipant{
		{JID: lid, LID: lid, PhoneNumber: pn, IsAdmin: true},
		{JID: lid},
		{JID: pn},
	})
	if got[0].JID != lid.String() || got[0].PhoneJID != pn.String() || got[0].LID != lid.String() || !got[0].IsAdmin {
		t.Errorf("full participant mapped wrong: %+v", got[0])
	}
	if got[1].PhoneJID != "" || got[1].LID != lid.String() {
		t.Errorf("lid-only participant mapped wrong: %+v", got[1])
	}
	if got[2].PhoneJID != pn.String() || got[2].LID != "" {
		t.Errorf("phone-only participant mapped wrong: %+v", got[2])
	}
}
