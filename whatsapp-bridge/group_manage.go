package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Group and community management, one endpoint with an action switch:
// POST /api/group-manage. Communities are groups with IsParent set; the
// announcement group of a community is created by the server.

const (
	groupNameMaxRunes        = 25
	groupDescriptionMaxRunes = 2048
	groupPhotoSize           = 640
)

// GroupManageRequest is the body of POST /api/group-manage. Which fields matter
// depends on Action; unused fields are ignored.
type GroupManageRequest struct {
	Action       string   `json:"action"`
	GroupJID     string   `json:"group_jid,omitempty"`
	ParentJID    string   `json:"parent_jid,omitempty"`
	ChildJID     string   `json:"child_jid,omitempty"`
	Name         string   `json:"name,omitempty"`
	Description  string   `json:"description,omitempty"`
	Participants []string `json:"participants,omitempty"`
	Value        *bool    `json:"value,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Reset        bool     `json:"reset,omitempty"`
	MediaPath    string   `json:"media_path,omitempty"`
	Code         string   `json:"code,omitempty"`
	// Optional settings applied right after create_group.
	Announce     *bool `json:"announce,omitempty"`
	Locked       *bool `json:"locked,omitempty"`
	JoinApproval *bool `json:"join_approval,omitempty"`
}

// GroupDetails is the shape returned for one group or community.
type GroupDetails struct {
	JID               string                 `json:"jid"`
	Name              string                 `json:"name"`
	Description       string                 `json:"description,omitempty"`
	IsCommunity       bool                   `json:"is_community"`
	ParentJID         string                 `json:"parent_jid,omitempty"`
	IsDefaultSub      bool                   `json:"is_announcement_group,omitempty"`
	AnnounceOnly      bool                   `json:"announce_only"`
	Locked            bool                   `json:"locked"`
	JoinApproval      bool                   `json:"join_approval_required"`
	MemberAddMode     string                 `json:"member_add_mode,omitempty"`
	ParticipantCount  int                    `json:"participant_count"`
	Owner             string                 `json:"owner,omitempty"`
	Created           string                 `json:"created,omitempty"`
	Participants      []GroupParticipantInfo `json:"participants,omitempty"`
	ParticipantErrors []GroupParticipantErr  `json:"participant_errors,omitempty"`
}

// GroupParticipantErr reports a member WhatsApp refused to add (privacy
// setting, not a contact, already in the group, ...).
type GroupParticipantErr struct {
	JID  string `json:"jid"`
	Code int    `json:"code"`
}

// GroupRequestInfo is one pending join request.
type GroupRequestInfo struct {
	JID         string `json:"jid"`
	RequestedAt string `json:"requested_at,omitempty"`
}

// GroupManageResponse is returned by POST /api/group-manage.
type GroupManageResponse struct {
	Success    bool                  `json:"success"`
	Message    string                `json:"message"`
	Group      *GroupDetails         `json:"group,omitempty"`
	Groups     []GroupDetails        `json:"groups,omitempty"`
	Results    []GroupParticipantErr `json:"results,omitempty"`
	Requests   []GroupRequestInfo    `json:"requests,omitempty"`
	InviteLink string                `json:"invite_link,omitempty"`
	PictureID  string                `json:"picture_id,omitempty"`
}

// groupAPI is the slice of *whatsmeow.Client used here; tests replace it.
type groupAPI interface {
	IsConnected() bool
	CreateGroup(ctx context.Context, req whatsmeow.ReqCreateGroup) (*types.GroupInfo, error)
	CreateCommunity(ctx context.Context, name, description string) (*types.GroupInfo, error)
	LinkGroup(ctx context.Context, parent, child types.JID) error
	UnlinkGroup(ctx context.Context, parent, child types.JID) error
	LeaveGroup(ctx context.Context, jid types.JID) error
	UpdateGroupParticipants(ctx context.Context, jid types.JID, changes []types.JID, action whatsmeow.ParticipantChange) ([]types.GroupParticipant, error)
	GetGroupRequestParticipants(ctx context.Context, jid types.JID) ([]types.GroupParticipantRequest, error)
	UpdateGroupRequestParticipants(ctx context.Context, jid types.JID, changes []types.JID, action whatsmeow.ParticipantRequestChange) ([]types.GroupParticipant, error)
	SetGroupPhoto(ctx context.Context, jid types.JID, avatar []byte) (string, error)
	SetGroupName(ctx context.Context, jid types.JID, name string) error
	SetGroupTopic(ctx context.Context, jid types.JID, previousID, newID, topic string) error
	SetGroupLocked(ctx context.Context, jid types.JID, locked bool) error
	SetGroupAnnounce(ctx context.Context, jid types.JID, announce bool) error
	SetGroupJoinApprovalMode(ctx context.Context, jid types.JID, mode bool) error
	SetGroupMemberAddMode(ctx context.Context, jid types.JID, mode types.GroupMemberAddMode) error
	GetGroupInviteLink(ctx context.Context, jid types.JID, reset bool) (string, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	GetSubGroups(ctx context.Context, community types.JID) ([]*types.GroupLinkTarget, error)
	GetGroupInfoFromLink(ctx context.Context, code string) (*types.GroupInfo, error)
	JoinGroupWithLink(ctx context.Context, code string) (types.JID, error)
}

// prepareGroupPhoto turns any image into the square 640x640 JPEG WhatsApp
// expects. It is a variable so tests can run without ffmpeg.
var prepareGroupPhoto = func(ctx context.Context, src string) ([]byte, error) {
	out, err := os.CreateTemp("", "group-photo-*.jpg")
	if err != nil {
		return nil, err
	}
	outPath := out.Name()
	_ = out.Close()
	defer os.Remove(outPath)
	filter := fmt.Sprintf("crop='min(iw,ih)':'min(iw,ih)',scale=%d:%d", groupPhotoSize, groupPhotoSize)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", src, "-vf", filter, "-frames:v", "1", "-q:v", "3", outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("convert image: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return os.ReadFile(outPath)
}

func parseGroupJID(raw string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(raw))
	if err != nil || jid.User == "" || jid.Server != types.GroupServer {
		return types.JID{}, fmt.Errorf("%q is not a group JID (expected ...@g.us)", raw)
	}
	return jid, nil
}

// parseMemberJIDs accepts phone numbers ("5585999990000", "+55 85 99999-0000")
// or full user JIDs and returns normalized, de-duplicated JIDs.
func parseMemberJIDs(raw []string) ([]types.JID, error) {
	seen := map[string]bool{}
	var out []types.JID
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		var jid types.JID
		if strings.Contains(item, "@") {
			parsed, err := types.ParseJID(item)
			if err != nil || parsed.User == "" || (parsed.Server != types.DefaultUserServer && parsed.Server != types.HiddenUserServer) {
				return nil, fmt.Errorf("invalid participant %q", item)
			}
			jid = parsed.ToNonAD()
		} else {
			digits := strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, item)
			if len(digits) < 8 {
				return nil, fmt.Errorf("invalid participant %q", item)
			}
			jid = types.NewJID(digits, types.DefaultUserServer)
		}
		if !seen[jid.String()] {
			seen[jid.String()] = true
			out = append(out, jid)
		}
	}
	return out, nil
}

func validateGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if n := utf8.RuneCountInString(name); n > groupNameMaxRunes {
		return "", fmt.Errorf("name has %d characters, WhatsApp allows %d", n, groupNameMaxRunes)
	}
	return name, nil
}

func groupDetails(info *types.GroupInfo, withParticipants bool) GroupDetails {
	d := GroupDetails{
		JID:              info.JID.String(),
		Name:             info.Name,
		Description:      info.Topic,
		IsCommunity:      info.IsParent,
		IsDefaultSub:     info.IsDefaultSubGroup,
		AnnounceOnly:     info.IsAnnounce,
		Locked:           info.IsLocked,
		JoinApproval:     info.IsJoinApprovalRequired,
		MemberAddMode:    string(info.MemberAddMode),
		ParticipantCount: info.ParticipantCount,
	}
	if d.ParticipantCount == 0 {
		d.ParticipantCount = len(info.Participants)
	}
	if !info.LinkedParentJID.IsEmpty() {
		d.ParentJID = info.LinkedParentJID.String()
	}
	if !info.OwnerPN.IsEmpty() {
		d.Owner = info.OwnerPN.String()
	} else if !info.OwnerJID.IsEmpty() {
		d.Owner = info.OwnerJID.String()
	}
	if !info.GroupCreated.IsZero() {
		d.Created = info.GroupCreated.UTC().Format(time.RFC3339)
	}
	if withParticipants {
		d.Participants = groupParticipantInfos(info.Participants)
	}
	for _, p := range info.Participants {
		if p.Error != 0 {
			d.ParticipantErrors = append(d.ParticipantErrors, GroupParticipantErr{JID: p.JID.String(), Code: p.Error})
		}
	}
	return d
}

func participantErrs(ps []types.GroupParticipant) []GroupParticipantErr {
	var out []GroupParticipantErr
	for _, p := range ps {
		if p.Error != 0 {
			out = append(out, GroupParticipantErr{JID: p.JID.String(), Code: p.Error})
		}
	}
	return out
}

// runGroupManage executes one action. status is the HTTP status to answer with.
func runGroupManage(ctx context.Context, api groupAPI, roots []string, req GroupManageRequest) (int, GroupManageResponse) {
	bad := func(format string, a ...any) (int, GroupManageResponse) {
		return http.StatusBadRequest, GroupManageResponse{Message: fmt.Sprintf(format, a...)}
	}
	fail := func(what string, err error) (int, GroupManageResponse) {
		return http.StatusBadGateway, GroupManageResponse{Message: fmt.Sprintf("%s: %v", what, err)}
	}
	ok := func(msg string) (int, GroupManageResponse) {
		return http.StatusOK, GroupManageResponse{Success: true, Message: msg}
	}

	if !api.IsConnected() {
		return http.StatusServiceUnavailable, GroupManageResponse{Message: "WhatsApp client is not connected. Please wait for reconnection."}
	}
	action := strings.TrimSpace(req.Action)

	needGroup := func() (types.JID, bool) {
		jid, err := parseGroupJID(req.GroupJID)
		return jid, err == nil
	}

	switch action {
	case "create_group", "create_community":
		name, err := validateGroupName(req.Name)
		if err != nil {
			return bad("%v", err)
		}
		if utf8.RuneCountInString(req.Description) > groupDescriptionMaxRunes {
			return bad("description is longer than %d characters", groupDescriptionMaxRunes)
		}
		members, err := parseMemberJIDs(req.Participants)
		if err != nil {
			return bad("%v", err)
		}
		if action == "create_community" {
			info, err := api.CreateCommunity(ctx, name, strings.TrimSpace(req.Description))
			if err != nil {
				return fail("Failed to create community", err)
			}
			note := ""
			if len(members) > 0 {
				res, err := api.UpdateGroupParticipants(ctx, info.JID, members, whatsmeow.ParticipantChangeAdd)
				if err != nil {
					note = fmt.Sprintf(" (created, but adding members failed: %v)", err)
				} else {
					info.Participants = append(info.Participants, res...)
				}
			}
			d := groupDetails(info, true)
			return http.StatusOK, GroupManageResponse{Success: true, Message: "Created " + info.JID.String() + note, Group: &d}
		}
		creq := whatsmeow.ReqCreateGroup{Name: name, Participants: members}
		{
			if strings.TrimSpace(req.ParentJID) != "" {
				parent, err := parseGroupJID(req.ParentJID)
				if err != nil {
					return bad("parent_jid: %v", err)
				}
				creq.LinkedParentJID = parent
			}
			if req.Announce != nil {
				creq.GroupAnnounce = types.GroupAnnounce{IsAnnounce: *req.Announce}
			}
			if req.Locked != nil {
				creq.GroupLocked = types.GroupLocked{IsLocked: *req.Locked}
			}
			if req.JoinApproval != nil {
				creq.GroupMembershipApprovalMode = types.GroupMembershipApprovalMode{IsJoinApprovalRequired: *req.JoinApproval}
			}
		}
		info, err := api.CreateGroup(ctx, creq)
		if err != nil {
			return fail("Failed to create "+strings.TrimPrefix(action, "create_"), err)
		}
		note := ""
		if strings.TrimSpace(req.Description) != "" {
			if err := api.SetGroupTopic(ctx, info.JID, "", "", strings.TrimSpace(req.Description)); err != nil {
				note = fmt.Sprintf(" (created, but the description failed: %v)", err)
			} else {
				info.Topic = strings.TrimSpace(req.Description)
			}
		}
		d := groupDetails(info, true)
		return http.StatusOK, GroupManageResponse{Success: true, Message: "Created " + info.JID.String() + note, Group: &d}

	case "link_group", "unlink_group":
		parent, err := parseGroupJID(req.ParentJID)
		if err != nil {
			return bad("parent_jid: %v", err)
		}
		child, err := parseGroupJID(req.ChildJID)
		if err != nil {
			return bad("child_jid: %v", err)
		}
		if action == "link_group" {
			err = api.LinkGroup(ctx, parent, child)
		} else {
			err = api.UnlinkGroup(ctx, parent, child)
		}
		if err != nil {
			return fail("Failed to "+strings.ReplaceAll(action, "_", " "), err)
		}
		return ok(fmt.Sprintf("%s done: %s in community %s", action, child, parent))

	case "add_participants", "remove_participants", "promote", "demote":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		members, err := parseMemberJIDs(req.Participants)
		if err != nil {
			return bad("%v", err)
		}
		if len(members) == 0 {
			return bad("participants is required")
		}
		change := map[string]whatsmeow.ParticipantChange{
			"add_participants":    whatsmeow.ParticipantChangeAdd,
			"remove_participants": whatsmeow.ParticipantChangeRemove,
			"promote":             whatsmeow.ParticipantChangePromote,
			"demote":              whatsmeow.ParticipantChangeDemote,
		}[action]
		res, err := api.UpdateGroupParticipants(ctx, jid, members, change)
		if err != nil {
			return fail("Failed to "+string(change)+" participants", err)
		}
		errs := participantErrs(res)
		msg := fmt.Sprintf("%s: %d requested, %d refused", string(change), len(members), len(errs))
		return http.StatusOK, GroupManageResponse{Success: true, Message: msg, Results: errs}

	case "rename":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		name, err := validateGroupName(req.Name)
		if err != nil {
			return bad("%v", err)
		}
		if err := api.SetGroupName(ctx, jid, name); err != nil {
			return fail("Failed to rename", err)
		}
		return ok("Renamed to " + name)

	case "set_description":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		desc := strings.TrimSpace(req.Description)
		if utf8.RuneCountInString(desc) > groupDescriptionMaxRunes {
			return bad("description is longer than %d characters", groupDescriptionMaxRunes)
		}
		if err := api.SetGroupTopic(ctx, jid, "", "", desc); err != nil {
			return fail("Failed to set description", err)
		}
		if desc == "" {
			return ok("Description removed")
		}
		return ok("Description updated")

	case "set_photo":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		if strings.TrimSpace(req.MediaPath) == "" {
			pid, err := api.SetGroupPhoto(ctx, jid, nil)
			if err != nil {
				return fail("Failed to remove photo", err)
			}
			return http.StatusOK, GroupManageResponse{Success: true, Message: "Photo removed", PictureID: pid}
		}
		resolved, err := validateMediaPath(req.MediaPath, roots)
		if err != nil {
			return http.StatusForbidden, GroupManageResponse{Message: err.Error()}
		}
		jpg, err := prepareGroupPhoto(ctx, resolved)
		if err != nil {
			return bad("%v", err)
		}
		pid, err := api.SetGroupPhoto(ctx, jid, jpg)
		if err != nil {
			return fail("Failed to set photo", err)
		}
		return http.StatusOK, GroupManageResponse{Success: true, Message: "Photo updated", PictureID: pid}

	case "set_announce", "set_locked", "set_join_approval":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		if req.Value == nil {
			return bad("value (true/false) is required")
		}
		var err error
		switch action {
		case "set_announce":
			err = api.SetGroupAnnounce(ctx, jid, *req.Value)
		case "set_locked":
			err = api.SetGroupLocked(ctx, jid, *req.Value)
		default:
			err = api.SetGroupJoinApprovalMode(ctx, jid, *req.Value)
		}
		if err != nil {
			return fail("Failed to "+strings.ReplaceAll(action, "_", " "), err)
		}
		return ok(fmt.Sprintf("%s = %v", action, *req.Value))

	case "set_member_add_mode":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		var mode types.GroupMemberAddMode
		switch strings.TrimSpace(req.Mode) {
		case "admin", string(types.GroupMemberAddModeAdmin):
			mode = types.GroupMemberAddModeAdmin
		case "all", string(types.GroupMemberAddModeAllMember):
			mode = types.GroupMemberAddModeAllMember
		default:
			return bad("mode must be \"admin\" or \"all\"")
		}
		if err := api.SetGroupMemberAddMode(ctx, jid, mode); err != nil {
			return fail("Failed to set member add mode", err)
		}
		return ok("Who can add members: " + string(mode))

	case "invite_link":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		link, err := api.GetGroupInviteLink(ctx, jid, req.Reset)
		if err != nil {
			return fail("Failed to get invite link", err)
		}
		msg := "Invite link"
		if req.Reset {
			msg = "Invite link reset; the old one no longer works"
		}
		return http.StatusOK, GroupManageResponse{Success: true, Message: msg, InviteLink: link}

	case "leave":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		if err := api.LeaveGroup(ctx, jid); err != nil {
			return fail("Failed to leave", err)
		}
		return ok("Left " + jid.String())

	case "info":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		info, err := api.GetGroupInfo(ctx, jid)
		if err != nil {
			return fail("Failed to get group info", err)
		}
		d := groupDetails(info, true)
		return http.StatusOK, GroupManageResponse{Success: true, Message: fmt.Sprintf("%d participants", d.ParticipantCount), Group: &d}

	case "list_groups":
		infos, err := api.GetJoinedGroups(ctx)
		if err != nil {
			return fail("Failed to list groups", err)
		}
		out := make([]GroupDetails, 0, len(infos))
		for _, info := range infos {
			out = append(out, groupDetails(info, false))
		}
		return http.StatusOK, GroupManageResponse{Success: true, Message: fmt.Sprintf("%d groups and communities", len(out)), Groups: out}

	case "subgroups":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid (the community) is required and must be ...@g.us")
		}
		subs, err := api.GetSubGroups(ctx, jid)
		if err != nil {
			return fail("Failed to list community groups", err)
		}
		out := make([]GroupDetails, 0, len(subs))
		for _, s := range subs {
			out = append(out, GroupDetails{JID: s.JID.String(), Name: s.Name, IsDefaultSub: s.IsDefaultSubGroup, ParentJID: jid.String()})
		}
		return http.StatusOK, GroupManageResponse{Success: true, Message: fmt.Sprintf("%d groups in the community", len(out)), Groups: out}

	case "list_requests":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		reqs, err := api.GetGroupRequestParticipants(ctx, jid)
		if err != nil {
			return fail("Failed to list join requests", err)
		}
		out := make([]GroupRequestInfo, 0, len(reqs))
		for _, r := range reqs {
			item := GroupRequestInfo{JID: r.JID.String()}
			if !r.RequestedAt.IsZero() {
				item.RequestedAt = r.RequestedAt.UTC().Format(time.RFC3339)
			}
			out = append(out, item)
		}
		return http.StatusOK, GroupManageResponse{Success: true, Message: fmt.Sprintf("%d pending requests", len(out)), Requests: out}

	case "approve_requests", "reject_requests":
		jid, valid := needGroup()
		if !valid {
			return bad("group_jid is required and must be ...@g.us")
		}
		members, err := parseMemberJIDs(req.Participants)
		if err != nil {
			return bad("%v", err)
		}
		if len(members) == 0 {
			return bad("participants is required")
		}
		change := whatsmeow.ParticipantChangeApprove
		if action == "reject_requests" {
			change = whatsmeow.ParticipantChangeReject
		}
		res, err := api.UpdateGroupRequestParticipants(ctx, jid, members, change)
		if err != nil {
			return fail("Failed to "+string(change)+" requests", err)
		}
		errs := participantErrs(res)
		return http.StatusOK, GroupManageResponse{Success: true, Message: fmt.Sprintf("%s: %d requested, %d refused", string(change), len(members), len(errs)), Results: errs}

	case "preview_link", "join_link":
		code := strings.TrimSpace(req.Code)
		code = strings.TrimPrefix(code, "https://chat.whatsapp.com/")
		code = strings.TrimPrefix(code, "http://chat.whatsapp.com/")
		code = strings.TrimPrefix(code, whatsmeow.InviteLinkPrefix)
		if code == "" || strings.ContainsAny(code, "/?# ") {
			return bad("code must be an invite code or a https://chat.whatsapp.com/... link")
		}
		if action == "preview_link" {
			info, err := api.GetGroupInfoFromLink(ctx, code)
			if err != nil {
				return fail("Failed to read invite", err)
			}
			d := groupDetails(info, false)
			return http.StatusOK, GroupManageResponse{Success: true, Message: "Invite preview (not joined)", Group: &d}
		}
		jid, err := api.JoinGroupWithLink(ctx, code)
		if err != nil {
			return fail("Failed to join", err)
		}
		return ok("Joined " + jid.String())

	default:
		return bad("unknown action %q", action)
	}
}

// liveGroupAPI is the real client plus the community creation whatsmeow's own
// CreateGroup gets wrong: WhatsApp answers bad-request when the create query
// for a community carries the member_add_mode, ephemeral and approval nodes it
// adds to every group.
type liveGroupAPI struct{ *whatsmeow.Client }

// communityCreateNode builds the query WhatsApp Web sends to create a community.
func communityCreateNode(reqID, name, descID, description string) waBinary.Node {
	content := []waBinary.Node{}
	if description != "" {
		content = append(content, waBinary.Node{
			Tag:     "description",
			Attrs:   waBinary.Attrs{"id": descID},
			Content: []waBinary.Node{{Tag: "body", Content: []byte(description)}},
		})
	}
	content = append(content,
		waBinary.Node{Tag: "parent", Attrs: waBinary.Attrs{"default_membership_approval_mode": "request_required"}},
		waBinary.Node{Tag: "allow_non_admin_sub_group_creation"},
		waBinary.Node{Tag: "create_general_chat"},
	)
	return waBinary.Node{
		Tag:   "iq",
		Attrs: waBinary.Attrs{"id": reqID, "xmlns": "w:g2", "type": "set", "to": types.GroupServerJID},
		Content: []waBinary.Node{{
			Tag:     "create",
			Attrs:   waBinary.Attrs{"subject": name},
			Content: content,
		}},
	}
}

func (l liveGroupAPI) CreateCommunity(ctx context.Context, name, description string) (*types.GroupInfo, error) {
	internals := l.DangerousInternals()
	reqID := internals.GenerateRequestID()
	waiter := internals.WaitResponse(reqID)
	if err := internals.SendNode(ctx, communityCreateNode(reqID, name, l.GenerateMessageID(), description)); err != nil {
		internals.CancelResponse(reqID, waiter)
		return nil, err
	}
	select {
	case <-ctx.Done():
		internals.CancelResponse(reqID, waiter)
		return nil, ctx.Err()
	case resp := <-waiter:
		if resp == nil {
			return nil, errors.New("connection closed while creating the community")
		}
		if errNode, ok := resp.GetOptionalChildByTag("error"); ok {
			return nil, fmt.Errorf("info query returned status %v: %v", errNode.AttrGetter().OptionalString("code"), errNode.AttrGetter().OptionalString("text"))
		}
		group, ok := resp.GetOptionalChildByTag("group")
		if !ok {
			return nil, errors.New("no group in the create response")
		}
		id, _ := group.Attrs["id"].(string)
		if id == "" {
			return nil, errors.New("create response has no group id")
		}
		return l.GetGroupInfo(ctx, types.NewJID(id, types.GroupServer))
	}
}

func registerGroupManageEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client, roots []string) {
	mux.HandleFunc("/api/group-manage", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GroupManageRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		status, resp := runGroupManage(ctx, liveGroupAPI{client}, roots, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
}
