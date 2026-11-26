package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

const maxNumbersPerLookup = 50

type CheckNumbersRequest struct {
	Phones []string `json:"phones"`
}

type NumberCheck struct {
	Phone        string `json:"phone"`
	JID          string `json:"jid,omitempty"`
	IsRegistered bool   `json:"is_registered"`
}

type CheckNumbersResponse struct {
	Success bool          `json:"success"`
	Message string        `json:"message"`
	Results []NumberCheck `json:"results,omitempty"`
}

var lookupNumbers = func(ctx context.Context, client *whatsmeow.Client, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	return client.IsOnWhatsApp(ctx, phones)
}

func toInternationalFormat(phone string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if digits == "" {
		return ""
	}
	return "+" + digits
}

func buildNumberChecks(phones []string, responses []types.IsOnWhatsAppResponse) []NumberCheck {
	byQuery := make(map[string]types.IsOnWhatsAppResponse, len(responses))
	for _, response := range responses {
		byQuery[response.Query] = response
	}
	checks := make([]NumberCheck, 0, len(phones))
	for _, phone := range phones {
		response, found := byQuery[phone]
		check := NumberCheck{Phone: strings.TrimPrefix(phone, "+")}
		if found && response.IsIn {
			check.IsRegistered = true
			check.JID = response.JID.ToNonAD().String()
		}
		checks = append(checks, check)
	}
	return checks
}

func registerCheckNumbersEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	mux.HandleFunc("/api/check-numbers", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req CheckNumbersRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}

		phones := make([]string, 0, len(req.Phones))
		for _, raw := range req.Phones {
			if phone := toInternationalFormat(raw); phone != "" {
				phones = append(phones, phone)
			}
		}
		if len(phones) == 0 || len(phones) > maxNumbersPerLookup {
			writeJSON(w, http.StatusBadRequest, CheckNumbersResponse{Message: "phones must hold between 1 and 50 numbers"})
			return
		}
		if !client.IsConnected() {
			writeJSON(w, http.StatusServiceUnavailable, CheckNumbersResponse{Message: "WhatsApp client is not connected. Please wait for reconnection."})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		responses, err := lookupNumbers(ctx, client, phones)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, CheckNumbersResponse{Message: "Failed to look up numbers: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, CheckNumbersResponse{Success: true, Message: "ok", Results: buildNumberChecks(phones, responses)})
	}))
}

func extractContactCardText(msg *waProto.Message) string {
	cards := []*waProto.ContactMessage{}
	if card := msg.GetContactMessage(); card != nil {
		cards = append(cards, card)
	}
	if array := msg.GetContactsArrayMessage(); array != nil {
		cards = append(cards, array.GetContacts()...)
	}

	lines := make([]string, 0, len(cards))
	for _, card := range cards {
		if vcard := strings.TrimSpace(card.GetVcard()); vcard != "" {
			lines = append(lines, vcard)
			continue
		}
		if name := card.GetDisplayName(); name != "" {
			lines = append(lines, "[Contact card] "+name)
		}
	}
	return strings.Join(lines, "\n")
}
