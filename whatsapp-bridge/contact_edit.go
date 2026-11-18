package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ContactEditRequest is the body of POST /api/contact-edit.
type ContactEditRequest struct {
	JID       string `json:"jid"`
	FullName  string `json:"full_name"`
	FirstName string `json:"first_name"`
}

// ContactEditResponse is returned by POST /api/contact-edit.
type ContactEditResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// buildContactEditPatch builds the app state patch that saves (or renames) a
// contact in the address book of the primary device. Contact names live in the
// critical_unblock_low collection under the "contact" index.
func buildContactEditPatch(jid types.JID, fullName, firstName string) appstate.PatchInfo {
	action := &waSyncAction.ContactAction{
		FullName:                 proto.String(fullName),
		FirstName:                proto.String(firstName),
		SaveOnPrimaryAddressbook: proto.Bool(true),
	}
	if jid.Server == types.HiddenUserServer {
		action.LidJID = proto.String(jid.String())
	} else {
		action.PnJID = proto.String(jid.String())
	}
	return appstate.PatchInfo{
		Type: appstate.WAPatchCriticalUnblockLow,
		Mutations: []appstate.MutationInfo{{
			Index:   []string{appstate.IndexContact, jid.String()},
			Version: 2,
			Value:   &waSyncAction.SyncActionValue{ContactAction: action},
		}},
	}
}

// sendContactEdit is a seam so tests can run without a live session.
var sendContactEdit = func(ctx context.Context, client *whatsmeow.Client, patch appstate.PatchInfo) error {
	return client.SendAppState(ctx, patch)
}

func registerContactEditEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	mux.HandleFunc("/api/contact-edit", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ContactEditRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(status int, resp ContactEditResponse) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
		}

		fullName := strings.TrimSpace(req.FullName)
		if fullName == "" {
			reply(http.StatusBadRequest, ContactEditResponse{Message: "full_name is required"})
			return
		}
		firstName := strings.TrimSpace(req.FirstName)
		if firstName == "" {
			firstName = strings.Fields(fullName)[0]
		}
		jid, err := types.ParseJID(req.JID)
		if err != nil || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
			reply(http.StatusBadRequest, ContactEditResponse{Message: fmt.Sprintf("Invalid user jid %q", req.JID)})
			return
		}
		if !client.IsConnected() {
			reply(http.StatusServiceUnavailable, ContactEditResponse{Message: "WhatsApp client is not connected. Please wait for reconnection."})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := sendContactEdit(ctx, client, buildContactEditPatch(jid.ToNonAD(), fullName, firstName)); err != nil {
			reply(http.StatusBadGateway, ContactEditResponse{Message: fmt.Sprintf("Failed to send contact edit: %v", err)})
			return
		}
		reply(http.StatusOK, ContactEditResponse{Success: true, Message: "Contact saved"})
	}))
}
