package main

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestToInternationalFormat(t *testing.T) {
	cases := map[string]string{
		"+55 (85) 99999-0000": "+5585999990000",
		"5585999990000":       "+5585999990000",
		"abc":                 "",
		"":                    "",
	}
	for input, want := range cases {
		if got := toInternationalFormat(input); got != want {
			t.Errorf("toInternationalFormat(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildNumberChecksMarksUnregisteredAndKeepsOrder(t *testing.T) {
	registered := types.NewJID("558599990000", types.DefaultUserServer)
	phones := []string{"+5585999990000", "+5585111110000", "+5585222220000"}
	responses := []types.IsOnWhatsAppResponse{
		{Query: "+5585999990000", JID: registered, IsIn: true},
		{Query: "+5585111110000", IsIn: false},
	}

	checks := buildNumberChecks(phones, responses)

	if len(checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(checks))
	}
	if !checks[0].IsRegistered || checks[0].JID != "558599990000@s.whatsapp.net" || checks[0].Phone != "5585999990000" {
		t.Errorf("unexpected first check: %+v", checks[0])
	}
	if checks[1].IsRegistered || checks[1].JID != "" {
		t.Errorf("unregistered number must not carry a jid: %+v", checks[1])
	}
	if checks[2].IsRegistered {
		t.Errorf("number missing from the response must be unregistered: %+v", checks[2])
	}
}

func TestExtractContactCardTextReturnsVcard(t *testing.T) {
	vcard := "BEGIN:VCARD\nVERSION:3.0\nFN:Romario\nTEL;type=CELL;waid=5585999990000:+55 85 99999-0000\nEND:VCARD"
	msg := &waProto.Message{ContactMessage: &waProto.ContactMessage{
		DisplayName: proto.String("Romario"),
		Vcard:       proto.String(vcard),
	}}

	if got := extractContactCardText(msg); got != vcard {
		t.Errorf("got %q, want the raw vcard", got)
	}
}

func TestExtractContactCardTextHandlesArrayAndMissingVcard(t *testing.T) {
	msg := &waProto.Message{ContactsArrayMessage: &waProto.ContactsArrayMessage{
		Contacts: []*waProto.ContactMessage{
			{Vcard: proto.String("BEGIN:VCARD\nFN:Ana\nEND:VCARD")},
			{DisplayName: proto.String("Bruno")},
		},
	}}

	want := "BEGIN:VCARD\nFN:Ana\nEND:VCARD\n[Contact card] Bruno"
	if got := extractContactCardText(msg); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractContactCardTextIgnoresOtherMessages(t *testing.T) {
	if got := extractContactCardText(&waProto.Message{Conversation: proto.String("hi")}); got != "" {
		t.Errorf("expected empty text, got %q", got)
	}
	if got := extractContactCardText(nil); got != "" {
		t.Errorf("expected empty text for nil message, got %q", got)
	}
}
