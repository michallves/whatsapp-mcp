package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Profile pictures are stored next to the other downloaded media, under
// store/profile-pictures. The file name carries the picture ID WhatsApp
// reports, so a cached file is always current: when the contact changes the
// photo the ID changes and a new file is fetched.
const (
	profilePictureDir      = "profile-pictures"
	profilePictureMaxBytes = 10 << 20
)

// ProfilePictureRequest is the body of POST /api/profile-picture.
type ProfilePictureRequest struct {
	JID     string `json:"jid"`
	Preview bool   `json:"preview"`
}

// ProfilePictureResponse is returned by POST /api/profile-picture.
// Status is one of ok, no_picture, private, rate_limited, not_connected,
// invalid_jid or error, so callers can react without parsing Message.
type ProfilePictureResponse struct {
	Success   bool   `json:"success"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Filename  string `json:"filename,omitempty"`
	Path      string `json:"path,omitempty"`
	PictureID string `json:"picture_id,omitempty"`
}

// GroupParticipantsRequest is the body of POST /api/group-participants.
type GroupParticipantsRequest struct {
	GroupJID string `json:"group_jid"`
}

// GroupParticipantInfo describes one member of a group. JID is the identifier
// WhatsApp uses for the member (often a LID); PhoneJID is set when the phone
// number is known, which is what the contact book and messages.db use.
type GroupParticipantInfo struct {
	JID      string `json:"jid"`
	PhoneJID string `json:"phone_jid,omitempty"`
	LID      string `json:"lid,omitempty"`
	IsAdmin  bool   `json:"is_admin"`
}

// GroupParticipantsResponse is returned by POST /api/group-participants.
type GroupParticipantsResponse struct {
	Success      bool                   `json:"success"`
	Message      string                 `json:"message"`
	GroupName    string                 `json:"group_name,omitempty"`
	Participants []GroupParticipantInfo `json:"participants,omitempty"`
}

// Seams so tests can exercise the handler logic without a live WhatsApp session.
var (
	getProfilePictureInfo = func(ctx context.Context, client *whatsmeow.Client, jid types.JID, preview bool) (*types.ProfilePictureInfo, error) {
		return client.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: preview})
	}
	fetchProfilePictureBytes = defaultFetchProfilePictureBytes
	getGroupInfoForPictures  = func(ctx context.Context, client *whatsmeow.Client, jid types.JID) (*types.GroupInfo, error) {
		return client.GetGroupInfo(ctx, jid)
	}
)

func defaultFetchProfilePictureBytes(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("unexpected profile picture URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image server answered HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, profilePictureMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > profilePictureMaxBytes {
		return nil, fmt.Errorf("profile picture larger than %d bytes", profilePictureMaxBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty profile picture")
	}
	return data, nil
}

// profilePictureStorePaths returns the directory and file for a picture. It
// reuses the single-component validation of the media store so a hostile JID
// or picture ID cannot escape store/.
func profilePictureStorePaths(jid types.JID, pictureID string, preview bool) (string, string, string, error) {
	jidComponent := strings.ReplaceAll(jid.String(), ":", "_")
	if err := validateStorePathComponent("JID", jidComponent); err != nil {
		return "", "", "", err
	}
	if err := validateStorePathComponent("picture ID", pictureID); err != nil {
		return "", "", "", err
	}
	suffix := ""
	if preview {
		suffix = "_preview"
	}
	filename := fmt.Sprintf("%s_%s%s.jpg", jidComponent, pictureID, suffix)

	storeRoot, err := filepath.Abs("store")
	if err != nil {
		return "", "", "", fmt.Errorf("resolve media store path: %w", err)
	}
	dir := filepath.Join(storeRoot, profilePictureDir)
	path := filepath.Join(dir, filename)
	if !pathWithin(storeRoot, dir) || !pathWithin(storeRoot, path) {
		return "", "", "", errors.New("profile picture path escapes store directory")
	}
	return dir, path, filename, nil
}

// downloadProfilePicture fetches the photo of a contact or group and returns
// the HTTP status to use together with the response body.
func downloadProfilePicture(ctx context.Context, client *whatsmeow.Client, jid types.JID, preview bool) (int, ProfilePictureResponse) {
	info, err := getProfilePictureInfo(ctx, client, jid, preview)
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		return http.StatusNotFound, ProfilePictureResponse{Status: "no_picture", Message: "This contact or group has no profile picture."}
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return http.StatusForbidden, ProfilePictureResponse{Status: "private", Message: "The contact hides the profile picture from you (privacy setting)."}
	case errors.Is(err, whatsmeow.ErrIQRateOverLimit):
		return http.StatusTooManyRequests, ProfilePictureResponse{Status: "rate_limited", Message: "WhatsApp is rate limiting profile picture requests. Wait a minute and try again."}
	case err != nil:
		return http.StatusBadGateway, ProfilePictureResponse{Status: "error", Message: fmt.Sprintf("Failed to get profile picture info: %v", err)}
	case info == nil || info.URL == "":
		return http.StatusNotFound, ProfilePictureResponse{Status: "no_picture", Message: "This contact or group has no profile picture."}
	}

	dir, path, filename, err := profilePictureStorePaths(jid, info.ID, preview)
	if err != nil {
		return http.StatusInternalServerError, ProfilePictureResponse{Status: "error", Message: err.Error()}
	}
	ok := ProfilePictureResponse{Success: true, Status: "ok", Filename: filename, Path: path, PictureID: info.ID, Message: "Profile picture downloaded"}

	if fileExists(path) {
		ok.Message = "Profile picture already downloaded"
		return http.StatusOK, ok
	}
	data, err := fetchProfilePictureBytes(ctx, info.URL)
	if err != nil {
		return http.StatusBadGateway, ProfilePictureResponse{Status: "error", Message: fmt.Sprintf("Failed to download profile picture: %v", err)}
	}
	if err := ensureOwnerOnlyDirectory(dir); err != nil {
		return http.StatusInternalServerError, ProfilePictureResponse{Status: "error", Message: fmt.Sprintf("failed to create profile picture directory: %v", err)}
	}
	if err := writeOwnerOnlyFile(path, data); err != nil {
		return http.StatusInternalServerError, ProfilePictureResponse{Status: "error", Message: fmt.Sprintf("failed to save profile picture: %v", err)}
	}
	return http.StatusOK, ok
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// registerProfilePictureEndpoints wires POST /api/profile-picture and
// POST /api/group-participants onto an existing mux.
func registerProfilePictureEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	mux.HandleFunc("/api/profile-picture", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ProfilePictureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if req.JID == "" {
			http.Error(w, "jid is required", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		reply := func(status int, resp ProfilePictureResponse) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
		}

		jid, err := types.ParseJID(req.JID)
		if err != nil || jid.User == "" {
			reply(http.StatusBadRequest, ProfilePictureResponse{Status: "invalid_jid", Message: fmt.Sprintf("Invalid jid %q", req.JID)})
			return
		}
		if !client.IsConnected() {
			reply(http.StatusServiceUnavailable, ProfilePictureResponse{Status: "not_connected", Message: "WhatsApp client is not connected. Please wait for reconnection."})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		status, resp := downloadProfilePicture(ctx, client, jid.ToNonAD(), req.Preview)
		reply(status, resp)
	}))

	mux.HandleFunc("/api/group-participants", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GroupParticipantsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		if req.GroupJID == "" {
			http.Error(w, "group_jid is required", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		reply := func(status int, resp GroupParticipantsResponse) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
		}

		jid, err := types.ParseJID(req.GroupJID)
		if err != nil || jid.Server != types.GroupServer {
			reply(http.StatusBadRequest, GroupParticipantsResponse{Message: fmt.Sprintf("group_jid %q is not a group JID (expected ...@g.us)", req.GroupJID)})
			return
		}
		if !client.IsConnected() {
			reply(http.StatusServiceUnavailable, GroupParticipantsResponse{Message: "WhatsApp client is not connected. Please wait for reconnection."})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		info, err := getGroupInfoForPictures(ctx, client, jid)
		if err != nil {
			reply(http.StatusBadGateway, GroupParticipantsResponse{Message: fmt.Sprintf("Failed to get group info: %v", err)})
			return
		}
		reply(http.StatusOK, GroupParticipantsResponse{
			Success:      true,
			Message:      fmt.Sprintf("%d participants", len(info.Participants)),
			GroupName:    info.Name,
			Participants: groupParticipantInfos(info.Participants),
		})
	}))
}

func groupParticipantInfos(participants []types.GroupParticipant) []GroupParticipantInfo {
	out := make([]GroupParticipantInfo, 0, len(participants))
	for _, p := range participants {
		item := GroupParticipantInfo{JID: p.JID.String(), IsAdmin: p.IsAdmin || p.IsSuperAdmin}
		if !p.PhoneNumber.IsEmpty() {
			item.PhoneJID = p.PhoneNumber.String()
		} else if p.JID.Server == types.DefaultUserServer {
			item.PhoneJID = p.JID.String()
		}
		if !p.LID.IsEmpty() {
			item.LID = p.LID.String()
		} else if p.JID.Server == types.HiddenUserServer {
			item.LID = p.JID.String()
		}
		out = append(out, item)
	}
	return out
}
