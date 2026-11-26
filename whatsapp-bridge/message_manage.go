package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// DeleteMessageRequest is the body of POST /api/delete-message.
// Revokes ("delete for everyone") a message. For messages sent by us leave
// SenderJID empty; to revoke someone else's message as group admin, pass the
// original sender's JID.
type DeleteMessageRequest struct {
	Recipient string `json:"recipient"`
	MessageID string `json:"message_id"`
	SenderJID string `json:"sender_jid"`
}

// EditMessageRequest is the body of POST /api/edit-message. Only text
// messages sent by us can be edited (WhatsApp allows it for ~15 minutes).
type EditMessageRequest struct {
	Recipient string `json:"recipient"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func registerMessageManageEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, store *MessageStore) {
	mux.HandleFunc("/api/delete-message", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req DeleteMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Recipient == "" || req.MessageID == "" {
			http.Error(w, "recipient and message_id are required", http.StatusBadRequest)
			return
		}
		chat, err := types.ParseJID(req.Recipient)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid recipient JID: %v", err), http.StatusBadRequest)
			return
		}
		sender := types.EmptyJID
		if req.SenderJID != "" {
			if sender, err = types.ParseJID(req.SenderJID); err != nil || sender.User == "" {
				http.Error(w, "Invalid sender_jid", http.StatusBadRequest)
				return
			}
		}
		if _, err := client.SendMessage(context.Background(), chat, client.BuildRevoke(chat, sender, req.MessageID)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if store != nil {
			_ = store.MarkMessageDeleted(req.MessageID, chat.String(), time.Now())
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("/api/edit-message", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req EditMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Recipient == "" || req.MessageID == "" || strings.TrimSpace(req.Text) == "" {
			http.Error(w, "recipient, message_id and text are required", http.StatusBadRequest)
			return
		}
		chat, err := types.ParseJID(req.Recipient)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid recipient JID: %v", err), http.StatusBadRequest)
			return
		}
		msg := client.BuildEdit(chat, req.MessageID, &waProto.Message{Conversation: proto.String(req.Text)})
		if _, err := client.SendMessage(context.Background(), chat, msg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if store != nil {
			_, _ = store.db.Exec(`UPDATE messages SET content = ? WHERE id = ? AND chat_jid = ? AND is_from_me = 1`, req.Text, req.MessageID, chat.String())
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
}
