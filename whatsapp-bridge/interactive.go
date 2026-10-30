package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// describeInteractive renders the interactive parts of a message (buttons,
// lists, quick replies, native-flow buttons / WhatsApp Flows, and the replies
// to them) as plain text, so they are stored and shown instead of being
// dropped as empty. Option ids are included because they are what
// /api/interactive-reply needs to answer. Flow response payloads
// (NativeFlowResponseMessage.ParamsJSON) are deliberately NOT rendered: they
// carry whatever the user typed into the form.
func describeInteractive(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	var lines []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			lines = append(lines, s)
		}
	}

	if ia := msg.GetInteractiveMessage(); ia != nil {
		add(ia.GetHeader().GetTitle())
		add(ia.GetHeader().GetSubtitle())
		add(ia.GetBody().GetText())
		add(ia.GetFooter().GetText())
		if nf := ia.GetNativeFlowMessage(); nf != nil {
			for i, b := range nf.GetButtons() {
				add(describeNativeFlowButton(i+1, b.GetName(), b.GetButtonParamsJSON()))
			}
		}
	}
	if lst := msg.GetListMessage(); lst != nil {
		add(lst.GetTitle())
		add(lst.GetDescription())
		add(lst.GetFooterText())
		if bt := lst.GetButtonText(); bt != "" {
			add("[Lista] botão: " + bt)
		}
		for _, sec := range lst.GetSections() {
			if t := sec.GetTitle(); t != "" {
				add("== " + t + " ==")
			}
			for _, row := range sec.GetRows() {
				l := fmt.Sprintf("- [id=%s] %s", row.GetRowID(), row.GetTitle())
				if d := row.GetDescription(); d != "" {
					l += " — " + d
				}
				add(l)
			}
		}
	}
	if bm := msg.GetButtonsMessage(); bm != nil {
		add(bm.GetContentText())
		add(bm.GetText())
		add(bm.GetFooterText())
		for _, b := range bm.GetButtons() {
			add(fmt.Sprintf("[Botão id=%s] %s", b.GetButtonID(), b.GetButtonText().GetDisplayText()))
		}
	}
	if tpl := msg.GetTemplateMessage(); tpl != nil {
		if h := tpl.GetHydratedTemplate(); h != nil {
			add(h.GetHydratedTitleText())
			add(h.GetHydratedContentText())
			add(h.GetHydratedFooterText())
			for _, b := range h.GetHydratedButtons() {
				switch {
				case b.GetQuickReplyButton() != nil:
					q := b.GetQuickReplyButton()
					add(fmt.Sprintf("[Botão id=%s index=%d] %s", q.GetID(), b.GetIndex(), q.GetDisplayText()))
				case b.GetUrlButton() != nil:
					add(fmt.Sprintf("[Link] %s → %s", b.GetUrlButton().GetDisplayText(), b.GetUrlButton().GetURL()))
				case b.GetCallButton() != nil:
					add(fmt.Sprintf("[Ligar] %s → %s", b.GetCallButton().GetDisplayText(), b.GetCallButton().GetPhoneNumber()))
				}
			}
		}
	}
	// Replies to interactive messages.
	if r := msg.GetButtonsResponseMessage(); r != nil {
		add(fmt.Sprintf("[Resposta de botão id=%s] %s", r.GetSelectedButtonID(), r.GetSelectedDisplayText()))
	}
	if r := msg.GetListResponseMessage(); r != nil {
		add(fmt.Sprintf("[Resposta de lista id=%s] %s", r.GetSingleSelectReply().GetSelectedRowID(), r.GetTitle()))
		add(r.GetDescription())
	}
	if r := msg.GetTemplateButtonReplyMessage(); r != nil {
		add(fmt.Sprintf("[Resposta de template id=%s] %s", r.GetSelectedID(), r.GetSelectedDisplayText()))
	}
	if r := msg.GetInteractiveResponseMessage(); r != nil {
		add(r.GetBody().GetText())
		if n := r.GetNativeFlowResponseMessage(); n != nil {
			add(fmt.Sprintf("[Resposta de fluxo: %s]", n.GetName()))
		}
	}
	return strings.Join(lines, "\n")
}

func describeNativeFlowButton(n int, name, paramsJSON string) string {
	var p map[string]any
	_ = json.Unmarshal([]byte(paramsJSON), &p)
	str := func(k string) string {
		if v, ok := p[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "quick_reply":
		return fmt.Sprintf("[Botão %d id=%s] %s", n, str("id"), str("display_text"))
	case "cta_url":
		return fmt.Sprintf("[Link %d] %s → %s", n, str("display_text"), str("url"))
	case "cta_call":
		return fmt.Sprintf("[Ligar %d] %s → %s", n, str("display_text"), str("phone_number"))
	case "cta_copy":
		return fmt.Sprintf("[Copiar %d] %s → %s", n, str("display_text"), str("copy_code"))
	case "single_select":
		// params carry the whole list: sections -> rows with id/title/description.
		var out []string
		out = append(out, fmt.Sprintf("[Lista %d] %s", n, str("title")))
		secs, _ := p["sections"].([]any)
		for _, s := range secs {
			sm, _ := s.(map[string]any)
			if t, _ := sm["title"].(string); t != "" {
				out = append(out, "== "+t+" ==")
			}
			rows, _ := sm["rows"].([]any)
			for _, r := range rows {
				rm, _ := r.(map[string]any)
				id, _ := rm["id"].(string)
				title, _ := rm["title"].(string)
				l := fmt.Sprintf("- [id=%s] %s", id, title)
				if d, _ := rm["description"].(string); d != "" {
					l += " — " + d
				}
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	case "galaxy_message", "flow", "open_flow":
		return fmt.Sprintf("[Flow %d] %s (formulário WhatsApp Flow; abrir/preencher exige o app do celular)", n, str("flow_cta"))
	default:
		return fmt.Sprintf("[Botão %d tipo=%s] %s", n, name, str("display_text"))
	}
}

// buildInteractiveReply builds the reply message for kind:
// "button" (legacy ButtonsMessage), "list" (ListMessage), "template"
// (template quick reply) or "native_flow" (quick_reply / single_select from an
// InteractiveMessage; also accepts a custom name + params_json).
func buildInteractiveReply(req InteractiveReplyRequest) (*waE2E.Message, error) {
	if req.OptionID == "" && req.Kind != "native_flow" {
		return nil, fmt.Errorf("option_id is required")
	}
	switch req.Kind {
	case "button":
		return &waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{
			SelectedButtonID: &req.OptionID,
			Response:         &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: req.Title},
			Type:             waE2E.ButtonsResponseMessage_DISPLAY_TEXT.Enum(),
			ContextInfo:      replyContext(req),
		}}, nil
	case "list":
		return &waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{
			Title:             &req.Title,
			ListType:          waE2E.ListResponseMessage_SINGLE_SELECT.Enum(),
			SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{SelectedRowID: &req.OptionID},
			ContextInfo:       replyContext(req),
		}}, nil
	case "template":
		idx := uint32(req.Index)
		return &waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{
			SelectedID:          &req.OptionID,
			SelectedDisplayText: &req.Title,
			SelectedIndex:       &idx,
			ContextInfo:         replyContext(req),
		}}, nil
	case "native_flow":
		name, params := req.Name, req.ParamsJSON
		if name == "" {
			name = "quick_reply"
		}
		if params == "" {
			b, _ := json.Marshal(map[string]string{"display_text": req.Title, "id": req.OptionID})
			params = string(b)
		}
		version := int32(3)
		return &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			Body: &waE2E.InteractiveResponseMessage_Body{
				Text:   &req.Title,
				Format: waE2E.InteractiveResponseMessage_Body_EXTENSIONS_1.Enum(),
			},
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
					Name:       &name,
					ParamsJSON: &params,
					Version:    &version,
				},
			},
			ContextInfo: replyContext(req),
		}}, nil
	}
	return nil, fmt.Errorf("unknown kind %q (use button, list, template or native_flow)", req.Kind)
}

func replyContext(req InteractiveReplyRequest) *waE2E.ContextInfo {
	if req.MessageID == "" {
		return nil
	}
	ci := &waE2E.ContextInfo{StanzaID: &req.MessageID}
	if req.SenderJID != "" {
		ci.Participant = &req.SenderJID
	}
	return ci
}

// InteractiveReplyRequest is the body of /api/interactive-reply.
type InteractiveReplyRequest struct {
	Recipient  string `json:"recipient"`
	Kind       string `json:"kind"`
	OptionID   string `json:"option_id"`
	Title      string `json:"title"`
	MessageID  string `json:"message_id"`
	SenderJID  string `json:"sender_jid"`
	Index      int    `json:"index"`
	Name       string `json:"name,omitempty"`
	ParamsJSON string `json:"params_json,omitempty"`
}
