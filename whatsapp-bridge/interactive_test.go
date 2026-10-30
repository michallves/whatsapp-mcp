package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestDescribeInteractiveNativeFlow(t *testing.T) {
	msg := &waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String("Escolha")},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
				{Name: proto.String("quick_reply"), ButtonParamsJSON: proto.String(`{"display_text":"Sim","id":"yes"}`)},
				{Name: proto.String("galaxy_message"), ButtonParamsJSON: proto.String(`{"flow_cta":"CLIQUE AQUI"}`)},
			}},
		},
	}}
	got := extractTextContent(msg)
	for _, want := range []string{"Escolha", "id=yes] Sim", "CLIQUE AQUI"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFlowResponseOmitsParams(t *testing.T) {
	msg := &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
		Body: &waE2E.InteractiveResponseMessage_Body{Text: proto.String("Resposta enviada")},
		InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
			NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
				Name: proto.String("flow"), ParamsJSON: proto.String(`{"cpf":"123"}`)},
		},
	}}
	got := extractTextContent(msg)
	if !strings.Contains(got, "Resposta enviada") || strings.Contains(got, "123") {
		t.Errorf("unexpected %q", got)
	}
}

func TestBuildInteractiveReply(t *testing.T) {
	for _, k := range []string{"button", "list", "template", "native_flow"} {
		if m, err := buildInteractiveReply(InteractiveReplyRequest{Kind: k, OptionID: "x", Title: "T"}); err != nil || m == nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	if _, err := buildInteractiveReply(InteractiveReplyRequest{Kind: "bogus", OptionID: "x"}); err == nil {
		t.Error("expected error")
	}
}
