package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type fakeGroupAPI struct {
	connected bool
	created   []whatsmeow.ReqCreateGroup
	calls     []string
	topic     string
	photo     []byte
	photoNil  bool
	err       error
	refuse    types.JID
}

func (f *fakeGroupAPI) rec(s string)      { f.calls = append(f.calls, s) }
func (f *fakeGroupAPI) IsConnected() bool { return f.connected }
func (f *fakeGroupAPI) CreateGroup(_ context.Context, r whatsmeow.ReqCreateGroup) (*types.GroupInfo, error) {
	f.created = append(f.created, r)
	if f.err != nil {
		return nil, f.err
	}
	info := &types.GroupInfo{JID: types.NewJID("120363000000000001", types.GroupServer)}
	info.Name = r.Name
	info.IsParent = r.IsParent
	info.LinkedParentJID = r.LinkedParentJID
	return info, nil
}
func (f *fakeGroupAPI) LinkGroup(_ context.Context, p, c types.JID) error {
	f.rec("link " + p.String() + " " + c.String())
	return f.err
}
func (f *fakeGroupAPI) UnlinkGroup(_ context.Context, p, c types.JID) error {
	f.rec("unlink " + p.String() + " " + c.String())
	return f.err
}
func (f *fakeGroupAPI) LeaveGroup(_ context.Context, j types.JID) error {
	f.rec("leave " + j.String())
	return f.err
}
func (f *fakeGroupAPI) UpdateGroupParticipants(_ context.Context, _ types.JID, ch []types.JID, a whatsmeow.ParticipantChange) ([]types.GroupParticipant, error) {
	f.rec(string(a))
	var out []types.GroupParticipant
	for _, j := range ch {
		p := types.GroupParticipant{JID: j}
		if j == f.refuse {
			p.Error = 403
		}
		out = append(out, p)
	}
	return out, f.err
}
func (f *fakeGroupAPI) GetGroupRequestParticipants(context.Context, types.JID) ([]types.GroupParticipantRequest, error) {
	return []types.GroupParticipantRequest{{JID: types.NewJID("5585999990000", types.DefaultUserServer)}}, f.err
}
func (f *fakeGroupAPI) UpdateGroupRequestParticipants(_ context.Context, _ types.JID, ch []types.JID, a whatsmeow.ParticipantRequestChange) ([]types.GroupParticipant, error) {
	f.rec(string(a))
	return nil, f.err
}
func (f *fakeGroupAPI) SetGroupPhoto(_ context.Context, _ types.JID, b []byte) (string, error) {
	f.photo, f.photoNil = b, b == nil
	return "pic1", f.err
}
func (f *fakeGroupAPI) SetGroupName(_ context.Context, _ types.JID, n string) error {
	f.rec("name " + n)
	return f.err
}
func (f *fakeGroupAPI) SetGroupTopic(_ context.Context, _ types.JID, _, _, t string) error {
	f.topic = t
	f.rec("topic")
	return f.err
}
func (f *fakeGroupAPI) SetGroupLocked(context.Context, types.JID, bool) error {
	f.rec("locked")
	return f.err
}
func (f *fakeGroupAPI) SetGroupAnnounce(context.Context, types.JID, bool) error {
	f.rec("announce")
	return f.err
}
func (f *fakeGroupAPI) SetGroupJoinApprovalMode(context.Context, types.JID, bool) error {
	f.rec("approval")
	return f.err
}
func (f *fakeGroupAPI) SetGroupMemberAddMode(_ context.Context, _ types.JID, m types.GroupMemberAddMode) error {
	f.rec("addmode " + string(m))
	return f.err
}
func (f *fakeGroupAPI) GetGroupInviteLink(_ context.Context, _ types.JID, reset bool) (string, error) {
	if reset {
		f.rec("reset")
	}
	return "https://chat.whatsapp.com/ABC", f.err
}
func (f *fakeGroupAPI) GetGroupInfo(_ context.Context, j types.JID) (*types.GroupInfo, error) {
	info := &types.GroupInfo{JID: j, Participants: []types.GroupParticipant{{JID: types.NewJID("5585999990000", types.DefaultUserServer), IsAdmin: true}}}
	info.Name = "G"
	return info, f.err
}
func (f *fakeGroupAPI) GetJoinedGroups(context.Context) ([]*types.GroupInfo, error) {
	a := &types.GroupInfo{JID: types.NewJID("1", types.GroupServer)}
	a.Name = "A"
	return []*types.GroupInfo{a}, f.err
}
func (f *fakeGroupAPI) GetSubGroups(context.Context, types.JID) ([]*types.GroupLinkTarget, error) {
	t := &types.GroupLinkTarget{JID: types.NewJID("2", types.GroupServer)}
	t.Name = "Sub"
	return []*types.GroupLinkTarget{t}, f.err
}
func (f *fakeGroupAPI) GetGroupInfoFromLink(_ context.Context, code string) (*types.GroupInfo, error) {
	f.rec("preview " + code)
	info := &types.GroupInfo{JID: types.NewJID("3", types.GroupServer)}
	return info, f.err
}
func (f *fakeGroupAPI) JoinGroupWithLink(_ context.Context, code string) (types.JID, error) {
	f.rec("join " + code)
	return types.NewJID("3", types.GroupServer), f.err
}

const testGroup = "120363000000000009@g.us"

func run(t *testing.T, f *fakeGroupAPI, roots []string, r GroupManageRequest) (int, GroupManageResponse) {
	t.Helper()
	return runGroupManage(context.Background(), f, roots, r)
}

func TestGroupManageNotConnected(t *testing.T) {
	st, resp := run(t, &fakeGroupAPI{}, nil, GroupManageRequest{Action: "list_groups"})
	if st != http.StatusServiceUnavailable || resp.Success {
		t.Fatalf("got %d %+v", st, resp)
	}
}

func TestGroupManageUnknownAction(t *testing.T) {
	st, _ := run(t, &fakeGroupAPI{connected: true}, nil, GroupManageRequest{Action: "explode"})
	if st != http.StatusBadRequest {
		t.Fatalf("got %d", st)
	}
}

func TestCreateGroupNormalizesMembersAndLinksParent(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	yes := true
	st, resp := run(t, f, nil, GroupManageRequest{
		Action: "create_group", Name: " Equipe ", Description: "desc",
		Participants: []string{"+55 85 99999-0000", "5585999990000@s.whatsapp.net", "558588887777"},
		ParentJID:    "120363000000000002@g.us", Announce: &yes,
	})
	if st != 200 || !resp.Success {
		t.Fatalf("got %d %+v", st, resp)
	}
	c := f.created[0]
	if c.Name != "Equipe" || len(c.Participants) != 2 {
		t.Fatalf("members not de-duplicated: %+v", c)
	}
	if c.Participants[0].String() != "5585999990000@s.whatsapp.net" {
		t.Fatalf("bad jid %s", c.Participants[0])
	}
	if c.LinkedParentJID.String() != "120363000000000002@g.us" || !c.IsAnnounce || c.IsParent {
		t.Fatalf("bad request %+v", c)
	}
	if f.topic != "desc" || resp.Group.Description != "desc" {
		t.Fatalf("description not applied: %q", f.topic)
	}
}

func TestCreateCommunity(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	st, resp := run(t, f, nil, GroupManageRequest{Action: "create_community", Name: "Comunidade", ParentJID: "ignored"})
	if st != 200 || !f.created[0].IsParent || !resp.Group.IsCommunity {
		t.Fatalf("got %d %+v", st, resp)
	}
	if !f.created[0].LinkedParentJID.IsEmpty() {
		t.Fatal("a community must not be linked to a parent")
	}
}

func TestCreateValidation(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	for name, req := range map[string]GroupManageRequest{
		"no name":    {Action: "create_group"},
		"long name":  {Action: "create_group", Name: strings.Repeat("a", 26)},
		"bad member": {Action: "create_group", Name: "x", Participants: []string{"12"}},
		"bad parent": {Action: "create_group", Name: "x", ParentJID: "5585@s.whatsapp.net"},
		"long desc":  {Action: "create_group", Name: "x", Description: strings.Repeat("d", 2049)},
	} {
		if st, _ := run(t, f, nil, req); st != http.StatusBadRequest {
			t.Errorf("%s: got %d", name, st)
		}
	}
	if len(f.created) != 0 {
		t.Fatal("nothing should have been created")
	}
}

func TestCreateEmoji25Runes(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "create_group", Name: strings.Repeat("é", 25)}); st != 200 {
		t.Fatalf("25 runes must pass, got %d", st)
	}
}

func TestCreateFailureIs502(t *testing.T) {
	f := &fakeGroupAPI{connected: true, err: errors.New("boom")}
	if st, resp := run(t, f, nil, GroupManageRequest{Action: "create_group", Name: "x"}); st != http.StatusBadGateway || resp.Success {
		t.Fatalf("got %d", st)
	}
}

func TestParticipantsReportRefusals(t *testing.T) {
	f := &fakeGroupAPI{connected: true, refuse: types.NewJID("5585999990000", types.DefaultUserServer)}
	st, resp := run(t, f, nil, GroupManageRequest{Action: "add_participants", GroupJID: testGroup, Participants: []string{"5585999990000", "5585988887777"}})
	if st != 200 || len(resp.Results) != 1 || resp.Results[0].Code != 403 {
		t.Fatalf("got %d %+v", st, resp)
	}
	for _, a := range []string{"remove_participants", "promote", "demote"} {
		if st, _ := run(t, f, nil, GroupManageRequest{Action: a, GroupJID: testGroup, Participants: []string{"5585999990000"}}); st != 200 {
			t.Errorf("%s: %d", a, st)
		}
	}
	if strings.Join(f.calls, ",") != "add,remove,promote,demote" {
		t.Fatalf("calls %v", f.calls)
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "promote", GroupJID: testGroup}); st != http.StatusBadRequest {
		t.Fatalf("empty participants must be 400, got %d", st)
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "promote", GroupJID: "5585@s.whatsapp.net", Participants: []string{"5585999990000"}}); st != http.StatusBadRequest {
		t.Fatalf("non-group jid must be 400, got %d", st)
	}
}

func TestLinkUnlink(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	req := GroupManageRequest{Action: "link_group", ParentJID: "120363000000000002@g.us", ChildJID: testGroup}
	if st, _ := run(t, f, nil, req); st != 200 {
		t.Fatal(st)
	}
	req.Action = "unlink_group"
	run(t, f, nil, req)
	if len(f.calls) != 2 || !strings.HasPrefix(f.calls[0], "link ") || !strings.HasPrefix(f.calls[1], "unlink ") {
		t.Fatalf("calls %v", f.calls)
	}
	req.ChildJID = ""
	if st, _ := run(t, f, nil, req); st != 400 {
		t.Fatalf("missing child must be 400, got %d", st)
	}
}

func TestSettings(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	yes := true
	for _, a := range []string{"set_announce", "set_locked", "set_join_approval"} {
		if st, _ := run(t, f, nil, GroupManageRequest{Action: a, GroupJID: testGroup, Value: &yes}); st != 200 {
			t.Errorf("%s: %d", a, st)
		}
		if st, _ := run(t, f, nil, GroupManageRequest{Action: a, GroupJID: testGroup}); st != 400 {
			t.Errorf("%s without value: %d", a, st)
		}
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "set_member_add_mode", GroupJID: testGroup, Mode: "all"}); st != 200 {
		t.Fatal(st)
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "set_member_add_mode", GroupJID: testGroup, Mode: "x"}); st != 400 {
		t.Fatal(st)
	}
	if got := f.calls[len(f.calls)-1]; got != "addmode all_member_add" {
		t.Fatalf("got %q", got)
	}
}

func TestRenameAndDescription(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "rename", GroupJID: testGroup, Name: "Novo"}); st != 200 {
		t.Fatal(st)
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "rename", GroupJID: testGroup, Name: ""}); st != 400 {
		t.Fatal(st)
	}
	f.topic = "x"
	if st, resp := run(t, f, nil, GroupManageRequest{Action: "set_description", GroupJID: testGroup}); st != 200 || f.topic != "" || resp.Message != "Description removed" {
		t.Fatalf("clearing description: %d %q", st, resp.Message)
	}
}

func TestSetPhoto(t *testing.T) {
	root := t.TempDir()
	img := filepath.Join(root, "foto.png")
	if err := os.WriteFile(img, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	old := prepareGroupPhoto
	defer func() { prepareGroupPhoto = old }()
	prepareGroupPhoto = func(_ context.Context, src string) ([]byte, error) {
		if !strings.HasSuffix(src, "foto.png") {
			t.Fatalf("unexpected src %s", src)
		}
		return []byte("jpeg"), nil
	}

	f := &fakeGroupAPI{connected: true}
	if st, resp := run(t, f, roots, GroupManageRequest{Action: "set_photo", GroupJID: testGroup, MediaPath: img}); st != 200 || string(f.photo) != "jpeg" || resp.PictureID != "pic1" {
		t.Fatalf("got %d %+v", st, resp)
	}
	if st, _ := run(t, f, roots, GroupManageRequest{Action: "set_photo", GroupJID: testGroup, MediaPath: "/etc/passwd"}); st != http.StatusForbidden {
		t.Fatalf("path outside roots must be 403, got %d", st)
	}
	if st, _ := run(t, f, roots, GroupManageRequest{Action: "set_photo", GroupJID: testGroup}); st != 200 || !f.photoNil {
		t.Fatalf("empty media_path must remove the photo, got %d", st)
	}
}

func TestInviteLinkAndLeave(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	st, resp := run(t, f, nil, GroupManageRequest{Action: "invite_link", GroupJID: testGroup, Reset: true})
	if st != 200 || resp.InviteLink != "https://chat.whatsapp.com/ABC" || f.calls[0] != "reset" {
		t.Fatalf("got %d %+v", st, resp)
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "leave", GroupJID: testGroup}); st != 200 {
		t.Fatal(st)
	}
}

func TestReadActions(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	if _, r := run(t, f, nil, GroupManageRequest{Action: "info", GroupJID: testGroup}); r.Group == nil || len(r.Group.Participants) != 1 || !r.Group.Participants[0].IsAdmin {
		t.Fatalf("%+v", r)
	}
	if _, r := run(t, f, nil, GroupManageRequest{Action: "list_groups"}); len(r.Groups) != 1 {
		t.Fatalf("%+v", r)
	}
	if _, r := run(t, f, nil, GroupManageRequest{Action: "subgroups", GroupJID: testGroup}); len(r.Groups) != 1 || r.Groups[0].ParentJID != testGroup {
		t.Fatalf("%+v", r)
	}
	if _, r := run(t, f, nil, GroupManageRequest{Action: "list_requests", GroupJID: testGroup}); len(r.Requests) != 1 {
		t.Fatalf("%+v", r)
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "approve_requests", GroupJID: testGroup, Participants: []string{"5585999990000"}}); st != 200 {
		t.Fatal(st)
	}
	if st, _ := run(t, f, nil, GroupManageRequest{Action: "reject_requests", GroupJID: testGroup, Participants: []string{"5585999990000"}}); st != 200 {
		t.Fatal(st)
	}
	if f.calls[len(f.calls)-2] != "approve" || f.calls[len(f.calls)-1] != "reject" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestJoinAndPreviewLink(t *testing.T) {
	f := &fakeGroupAPI{connected: true}
	run(t, f, nil, GroupManageRequest{Action: "preview_link", Code: "https://chat.whatsapp.com/ABC123"})
	run(t, f, nil, GroupManageRequest{Action: "join_link", Code: "ABC123"})
	if strings.Join(f.calls, ",") != "preview ABC123,join ABC123" {
		t.Fatalf("calls %v", f.calls)
	}
	for _, bad := range []string{"", "a/b", "ABC DEF", "https://evil.example/ABC"} {
		if st, _ := run(t, f, nil, GroupManageRequest{Action: "join_link", Code: bad}); st != 400 {
			t.Errorf("code %q: got %d", bad, st)
		}
	}
}
