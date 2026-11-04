import os
import signal
import sys
import tempfile
from typing import Annotated, Any

import requests
from mcp.server.fastmcp import FastMCP, Image
from pydantic import Field

import media_preview
import transcription
from mcp_config import resolve_host, resolve_port, resolve_transport
from parent_watchdog import install_stdio_parent_watchdog
from whatsapp import (
    MESSAGES_DB_PATH,
    msg_to_dict,
)
from whatsapp import (
    _fetch_image_url as whatsapp_fetch_image_url,
)
from whatsapp import (
    download_media as whatsapp_download_media,
)
from whatsapp import (
    get_chat as whatsapp_get_chat,
)
from whatsapp import (
    get_contact_chats as whatsapp_get_contact_chats,
)
from whatsapp import (
    get_direct_chat_by_contact as whatsapp_get_direct_chat_by_contact,
)
from whatsapp import (
    get_group_profile_pictures as whatsapp_get_group_profile_pictures,
)
from whatsapp import (
    get_last_interaction as whatsapp_get_last_interaction,
)
from whatsapp import (
    get_message_context as whatsapp_get_message_context,
)
from whatsapp import (
    get_profile_picture as whatsapp_get_profile_picture,
)
from whatsapp import (
    get_sender_name as whatsapp_get_sender_name,
)
from whatsapp import (
    group_manage as whatsapp_group_manage,
)
from whatsapp import (
    list_chats as whatsapp_list_chats,
)
from whatsapp import (
    list_contact_aliases as whatsapp_list_contact_aliases,
)
from whatsapp import (
    list_messages as whatsapp_list_messages,
)
from whatsapp import (
    mark_messages_read as whatsapp_mark_messages_read,
)
from whatsapp import (
    search_contacts as whatsapp_search_contacts,
)
from whatsapp import (
    send_audio_message as whatsapp_audio_voice_message,
)
from whatsapp import (
    send_file as whatsapp_send_file,
)
from whatsapp import (
    send_interactive_reply as whatsapp_send_interactive_reply,
)
from whatsapp import (
    send_message as whatsapp_send_message,
)
from whatsapp import (
    send_reaction as whatsapp_send_reaction,
)
from whatsapp import (
    set_contact_alias as whatsapp_set_contact_alias,
)
from whatsapp import (
    stage_image_bytes as whatsapp_stage_image_bytes,
)

# Initialize FastMCP server. Env-var handling is deferred to the __main__ block
# so importing this module never parses env vars or exits the process.
mcp = FastMCP("whatsapp")

PreviewDimension = Annotated[
    int,
    Field(
        strict=True,
        ge=media_preview.MIN_MAX_DIMENSION,
        le=media_preview.MAX_MAX_DIMENSION,
        description="Longest preview edge in pixels; must be an integer from 1 to 2048.",
    ),
]


@mcp.tool()
def search_contacts(query: str) -> list[dict[str, Any]]:
    """Search WhatsApp contacts by name or phone number.

    Args:
        query: Search term to match against contact names or phone numbers
    """
    contacts = whatsapp_search_contacts(query)
    return contacts


@mcp.tool()
def get_contact(
    identifier: str | None = None,
    phone_number: str | None = None,
    phone: str | None = None,
) -> dict[str, Any]:
    """Look up a WhatsApp contact by phone number, LID, or full JID.

    Automatically detects the identifier type and queries appropriately.

    Args:
        identifier: Phone number, LID, or full JID. Examples:
                    - "12025551234" (phone number)
                    - "35047067385985" (LID - numeric)
                    - "12025551234@s.whatsapp.net" (phone JID)
                    - "184125298348272@lid" (LID JID)
        phone_number: Backward-compatible alias for `identifier`.
        phone: Backward-compatible alias for `identifier` (matches README parameter name).

    Returns:
        Dictionary with jid, name, display_name, is_lid, and resolved status
    """
    if identifier is None:
        identifier = phone_number
    if identifier is None:
        identifier = phone
    if identifier is None:
        raise ValueError("Missing required argument: identifier (or phone_number / phone)")

    identifier = identifier.strip()
    if not identifier:
        raise ValueError("identifier must be non-empty")

    # Detect identifier type and normalize to JID.
    bare_numeric_digits: str | None = None
    if "@" in identifier:
        # Already a JID - use as-is
        jid = identifier
        is_lid = jid.endswith("@lid") or jid.split("@", 1)[-1] == "lid"
    else:
        digits = "".join(c for c in identifier if c.isdigit())
        if digits:
            # LIDs can overlap phone-number lengths, so bare numeric inputs try phone first.
            jid = f"{digits}@s.whatsapp.net"
            is_lid = False
            if identifier.isdigit():
                bare_numeric_digits = digits
        else:
            # Non-numeric and not a JID; try as-is.
            jid = identifier
            is_lid = False

    jid_user = jid.split("@", 1)[0]

    display_name: str | None = None
    resolved = False

    # Prefer chats table lookup via get_chat (works for both phone and LID contacts).
    candidates: list[tuple[str, bool]] = [(jid, is_lid)]
    if bare_numeric_digits:
        candidates.append((f"{bare_numeric_digits}@lid", True))

    chat = None
    for candidate_jid, candidate_is_lid in candidates:
        chat = whatsapp_get_chat(candidate_jid, include_last_message=False)
        if chat:
            jid = candidate_jid
            is_lid = candidate_is_lid
            jid_user = jid.split("@", 1)[0]
            break

    if chat and chat.get("name"):
        display_name = chat["name"]
        resolved = display_name not in (jid, jid_user)
    else:
        # Fallback: best-effort sender-name resolution (may use fuzzy LIKE lookup).
        display_name = whatsapp_get_sender_name(jid)
        resolved = display_name not in (jid, jid_user, identifier)

    return {
        "identifier": identifier,
        "jid": jid,
        "phone_number": jid_user if not is_lid else None,
        "lid": jid_user if is_lid else None,
        "name": display_name if resolved else jid_user,
        "display_name": display_name,
        "is_lid": is_lid,
        "resolved": resolved,
    }


@mcp.tool()
def list_messages(
    after: str | None = None,
    before: str | None = None,
    sender_phone_number: str | None = None,
    chat_jid: str | None = None,
    query: str | None = None,
    limit: int = 50,
    page: int = 0,
    include_context: bool = True,
    context_before: int = 1,
    context_after: int = 1,
    sort_by: str = "newest",
) -> list[dict[str, Any]]:
    """Get WhatsApp messages matching specified criteria with optional context.

    Each message includes sender_display showing "Name (phone)" for easy identification.

    Args:
        after: ISO-8601 date string (e.g., "2026-01-01" or "2026-01-01T09:00:00")
        before: ISO-8601 date string (e.g., "2026-01-09" or "2026-01-09T18:00:00")
        sender_phone_number: Phone number to filter by sender (e.g., "12025551234")
        chat_jid: Chat JID to filter by (e.g., "12025551234@s.whatsapp.net" or group JID)
        query: Search term to filter messages by content
        limit: Max messages to return (default 50, max 500)
        page: Page number for pagination (default 0)
        include_context: Include surrounding messages for context (default True)
        context_before: Messages to include before each match (default 1)
        context_after: Messages to include after each match (default 1)
        sort_by: "newest" (default, most recent first) or "oldest" (chronological)
    """
    # Cap limit at 500 to prevent excessive queries
    limit = min(limit, 500)
    messages = whatsapp_list_messages(
        after=after,
        before=before,
        sender_phone_number=sender_phone_number,
        chat_jid=chat_jid,
        query=query,
        limit=limit,
        page=page,
        include_context=include_context,
        context_before=context_before,
        context_after=context_after,
        sort_by=sort_by,
    )
    return messages


@mcp.tool()
def list_chats(
    query: str | None = None,
    limit: int = 50,
    page: int = 0,
    include_last_message: bool = True,
    sort_by: str = "last_active",
) -> list[dict[str, Any]]:
    """Get WhatsApp chats matching specified criteria.

    Args:
        query: Search term to filter chats by name or JID
        limit: Max chats to return (default 50, max 200)
        page: Page number for pagination (default 0)
        include_last_message: Include the last message in each chat (default True)
        sort_by: "last_active" (default, most recent first) or "name" (alphabetical)

    Returns:
        Chat dictionaries with jid, name, is_group, last_message_time, last_message,
        last_sender, last_is_from_me, last_read_time and unread. `last_read_time` is
        how far the chat has been read on any device (null if never reported); `unread`
        is true when the last message is inbound and newer than that marker, so chats
        already read on the phone are not reported as unread.
    """
    # Cap limit at 200 to prevent excessive queries
    limit = min(limit, 200)
    chats = whatsapp_list_chats(
        query=query, limit=limit, page=page, include_last_message=include_last_message, sort_by=sort_by
    )
    return chats


@mcp.tool()
def get_chat(chat_jid: str, include_last_message: bool = True) -> dict[str, Any]:
    """Get WhatsApp chat metadata by JID.

    Args:
        chat_jid: The JID of the chat to retrieve
        include_last_message: Whether to include the last message (default True)

    Returns:
        Chat dictionary — same shape as list_chats, including last_read_time and unread.
    """
    chat = whatsapp_get_chat(chat_jid, include_last_message)
    return chat


@mcp.tool()
def get_direct_chat_by_contact(sender_phone_number: str) -> dict[str, Any]:
    """Get WhatsApp chat metadata by sender phone number.

    Args:
        sender_phone_number: The phone number to search for
    """
    chat = whatsapp_get_direct_chat_by_contact(sender_phone_number)
    return chat


@mcp.tool()
def get_contact_chats(jid: str, limit: int = 20, page: int = 0) -> list[dict[str, Any]]:
    """Get all WhatsApp chats involving the contact.

    Args:
        jid: The contact's JID to search for
        limit: Maximum number of chats to return (default 20)
        page: Page number for pagination (default 0)
    """
    chats = whatsapp_get_contact_chats(jid, limit, page)
    return chats


@mcp.tool()
def get_last_interaction(jid: str) -> dict[str, Any]:
    """Get most recent WhatsApp message involving the contact.

    Args:
        jid: The JID of the contact to search for

    Returns:
        Message dictionary with id, timestamp, sender, content, etc. or empty dict if not found.
    """
    message = whatsapp_get_last_interaction(jid)
    return message if message else {}


@mcp.tool()
def get_message_context(message_id: str, before: int = 5, after: int = 5) -> dict[str, Any]:
    """Get context around a specific WhatsApp message.

    Args:
        message_id: The ID of the message to get context for
        before: Number of messages to include before the target message (default 5)
        after: Number of messages to include after the target message (default 5)
    """
    context = whatsapp_get_message_context(message_id, before, after)
    return {
        "message": msg_to_dict(context.message),
        "before": [msg_to_dict(message) for message in context.before],
        "after": [msg_to_dict(message) for message in context.after],
    }


@mcp.tool()
def send_message(
    recipient: str,
    message: str,
    quoted_message_id: str = "",
    quoted_sender_jid: str = "",
    quoted_content: str = "",
    mentions: list[str] | None = None,
) -> dict[str, Any]:
    """Send a WhatsApp message to a person or group. For group chats use the JID.

    Args:
        recipient: The recipient - either a phone number with country code but no + or other symbols,
                 or a JID (e.g., "123456789@s.whatsapp.net" or a group JID like "123456789@g.us")
        message: The message text to send
        quoted_message_id: ID of the message to reply to (optional). When set, the sent
                           message will appear as a quoted reply in WhatsApp.
        quoted_sender_jid: Full JID of the author of the quoted message. Required for
                           group replies so WhatsApp renders the correct attribution.
        quoted_content: Text content of the quoted message, used for the reply preview.
                        Only plain text is supported; media previews are not included.
        mentions: Users to @-mention, as phone numbers with country code but no + (e.g.
                  ["420601234567"]) or JIDs. For each entry the message text must contain
                  a matching "@<number>" token (e.g. "hi @420601234567"), otherwise the
                  mention won't render on recipients' devices. Only meaningful in groups.

    Returns:
        A dictionary containing success status and a status message
    """
    # Validate input
    if not recipient:
        return {"success": False, "message": "Recipient must be provided"}

    # Call the whatsapp_send_message function with the unified recipient parameter
    success, status_message = whatsapp_send_message(
        recipient, message, quoted_message_id, quoted_sender_jid, quoted_content, mentions
    )
    return {"success": success, "message": status_message}


@mcp.tool()
def send_reaction(
    recipient: str,
    message_id: str,
    emoji: str,
    from_me: bool = False,
    sender_jid: str = "",
) -> dict[str, Any]:
    """Send (or remove) a reaction to a WhatsApp message.

    Args:
        recipient: The chat JID the message belongs to (e.g., "12025551234@s.whatsapp.net"
                   or a group JID like "123456789@g.us")
        message_id: The ID of the message to react to
        emoji: The reaction emoji (e.g., "👍"). Pass an empty string to remove the reaction.
        from_me: Whether the original message was sent by the current user (default False)
        sender_jid: JID of the original message sender — required for group messages when
                    from_me is False so the bridge can build the correct WhatsApp key

    Returns:
        A dictionary containing success status and a status message
    """
    success, status_message = whatsapp_send_reaction(recipient, message_id, emoji, from_me, sender_jid)
    return {"success": success, "message": status_message}


@mcp.tool()
def reply_to_interactive(
    recipient: str,
    kind: str,
    option_id: str = "",
    title: str = "",
    message_id: str = "",
    sender_jid: str = "",
    index: int = 0,
    name: str = "",
    params_json: str = "",
) -> dict[str, Any]:
    """Answer an interactive message (buttons, lists, quick replies, templates).

    Interactive messages are shown in list_messages with markers such as
    "[Botão id=abc] Sim" or "- [id=row1] Opção". Use those ids here.

    Args:
        recipient: Chat JID of the conversation
        kind: "button" (legacy buttons), "list" (list row), "template" (template quick
              reply) or "native_flow" (quick_reply / list from native-flow messages; most
              modern business bots)
        option_id: The id shown in the marker of the chosen option
        title: The visible text of the chosen option
        message_id: ID of the interactive message being answered (recommended)
        sender_jid: JID of that message's sender (needed in groups)
        index: Button index, only for kind="template"
        name: native_flow only; response name (default "quick_reply")
        params_json: native_flow only; raw response params, overrides the default
                     {"display_text","id"} payload. Does not open or fill WhatsApp Flow
                     forms; Flows must be completed on the phone.
    """
    success, status_message = whatsapp_send_interactive_reply(
        recipient, kind, option_id, title, message_id, sender_jid, index, name, params_json
    )
    return {"success": success, "message": status_message}


@mcp.tool()
def mark_messages_read(
    message_ids: list[str],
    chat_jid: str,
    sender_jid: str = "",
    timestamp: str | None = None,
) -> dict[str, Any]:
    """Mark selected WhatsApp messages as read and send read receipts.

    This is an explicit external side effect. All message IDs must belong to the
    same chat and sender.

    Args:
        message_ids: IDs of the messages to mark as read
        chat_jid: JID of the chat containing the messages
        sender_jid: JID or bare phone number of the original sender; required for groups
        timestamp: Optional RFC 3339 read timestamp; defaults to the current time

    Returns:
        A dictionary containing success status and a status message
    """
    success, status_message = whatsapp_mark_messages_read(message_ids, chat_jid, sender_jid, timestamp)
    return {"success": success, "message": status_message}


@mcp.tool()
def send_file(recipient: str, media_path: str, caption: str = "") -> dict[str, Any]:
    """Send a file (image, video, document) via WhatsApp, optionally with a caption.

    When `caption` is provided, the file and text arrive as a single
    attachment-with-caption message (one bubble in the WA UI), instead of
    needing a separate follow-up send_message call. For group chats use the JID.

    Args:
        recipient: Either a phone number with country code (no + or symbols),
                 or a JID (e.g., "123456789@s.whatsapp.net" or "123456789@g.us")
        media_path: Absolute path to the media file (image, video, document)
        caption: Optional text rendered with the file as a caption. Omit for a
                 bare attachment.

    Returns:
        A dictionary containing success status and a status message
    """

    # Call the whatsapp_send_file function
    success, status_message = whatsapp_send_file(recipient, media_path, caption)
    return {"success": success, "message": status_message}


@mcp.tool()
def send_audio_message(recipient: str, media_path: str) -> dict[str, Any]:
    """Send any audio file as a WhatsApp audio message to the specified recipient. For group messages use the JID. If it errors due to ffmpeg not being installed, use send_file instead.

    Args:
        recipient: The recipient - either a phone number with country code but no + or other symbols,
                 or a JID (e.g., "123456789@s.whatsapp.net" or a group JID like "123456789@g.us")
        media_path: The absolute path to the audio file to send (will be converted to Opus .ogg if it's not a .ogg file)

    Returns:
        A dictionary containing success status and a status message
    """
    success, status_message = whatsapp_audio_voice_message(recipient, media_path)
    return {"success": success, "message": status_message}


@mcp.tool()
def set_contact_alias(jid: str, alias: str) -> dict[str, Any]:
    """Set the display name the MCP shows for a contact, e.g. "Manoel Lopes - ALMA LABS - Coord".

    The alias is kept only by the MCP (contact_aliases.json next to messages.db). It does
    not change the phone's address book. It is used by search_contacts, list_chats and
    message senders, and searching also matches alias text such as the project or role.
    An empty alias removes it.

    Args:
        jid: Contact JID (number@s.whatsapp.net or number@lid) or phone number
        alias: Name to show; empty to remove the alias
    """
    return whatsapp_set_contact_alias(jid, alias)


@mcp.tool()
def list_contact_aliases() -> dict[str, str]:
    """List every contact alias as {phone number: display name}."""
    return whatsapp_list_contact_aliases()


# --- Groups and communities -------------------------------------------------
# Every tool below goes through POST /api/group-manage on the bridge. Creating
# groups, adding people and changing settings notifies the members: only do it
# when the user asked for that exact change.


@mcp.tool()
def list_groups() -> dict[str, Any]:
    """List every group and community this account is in (name, JID, is_community, parent)."""
    return whatsapp_group_manage("list_groups")


@mcp.tool()
def get_group_info(group_jid: str) -> dict[str, Any]:
    """Get a group or community: name, description, settings, owner, parent community and members (with admin flag).

    Args:
        group_jid: Group JID (id@g.us)
    """
    return whatsapp_group_manage("info", group_jid=group_jid)


@mcp.tool()
def create_group(
    name: str,
    participants: list[str],
    description: str | None = None,
    community_jid: str | None = None,
    announce_only: bool | None = None,
    admins_edit_info_only: bool | None = None,
    require_join_approval: bool | None = None,
) -> dict[str, Any]:
    """Create a WhatsApp group. Members are notified, so only do it when the user asked.

    Args:
        name: Group name, at most 25 characters
        participants: Phone numbers (5585999990000) or JIDs to add; you are added automatically.
            WhatsApp refuses some (privacy settings, not a contact): those come back in "results"
            or "group.participant_errors" with the error code
        description: Optional group description
        community_jid: Optional community JID (id@g.us); creates the group inside that community
        announce_only: Only admins can send messages
        admins_edit_info_only: Only admins can edit the group info
        require_join_approval: New members need admin approval
    """
    return whatsapp_group_manage(
        "create_group",
        name=name,
        participants=participants,
        description=description,
        parent_jid=community_jid,
        announce=announce_only,
        locked=admins_edit_info_only,
        join_approval=require_join_approval,
    )


@mcp.tool()
def create_community(
    name: str, description: str | None = None, participants: list[str] | None = None
) -> dict[str, Any]:
    """Create a WhatsApp community. WhatsApp also creates its announcement group automatically.

    Add existing groups with community_link_group, or new ones with create_group(community_jid=...).

    Args:
        name: Community name, at most 25 characters
        description: Optional description
        participants: Optional phone numbers or JIDs to add as members
    """
    return whatsapp_group_manage(
        "create_community", name=name, description=description, participants=participants or []
    )


@mcp.tool()
def community_link_group(community_jid: str, group_jid: str) -> dict[str, Any]:
    """Add an existing group to a community (you must be admin of both).

    Args:
        community_jid: Community JID (id@g.us)
        group_jid: Group to add (id@g.us)
    """
    return whatsapp_group_manage("link_group", parent_jid=community_jid, child_jid=group_jid)


@mcp.tool()
def community_unlink_group(community_jid: str, group_jid: str) -> dict[str, Any]:
    """Remove a group from a community. The group itself stays; it just stops being part of it.

    Args:
        community_jid: Community JID (id@g.us)
        group_jid: Group to remove (id@g.us)
    """
    return whatsapp_group_manage("unlink_group", parent_jid=community_jid, child_jid=group_jid)


@mcp.tool()
def list_community_groups(community_jid: str) -> dict[str, Any]:
    """List the groups that belong to a community (the announcement group is flagged).

    Args:
        community_jid: Community JID (id@g.us)
    """
    return whatsapp_group_manage("subgroups", group_jid=community_jid)


@mcp.tool()
def add_group_participants(group_jid: str, participants: list[str]) -> dict[str, Any]:
    """Add people to a group or community. Needs admin, and notifies them.

    Args:
        group_jid: Group JID (id@g.us)
        participants: Phone numbers or JIDs. Refusals are listed in "results" with WhatsApp's error code
    """
    return whatsapp_group_manage("add_participants", group_jid=group_jid, participants=participants)


@mcp.tool()
def remove_group_participants(group_jid: str, participants: list[str]) -> dict[str, Any]:
    """Remove people from a group or community. Needs admin.

    Args:
        group_jid: Group JID (id@g.us)
        participants: Phone numbers or JIDs to remove
    """
    return whatsapp_group_manage("remove_participants", group_jid=group_jid, participants=participants)


@mcp.tool()
def set_group_admins(group_jid: str, participants: list[str], admin: bool = True) -> dict[str, Any]:
    """Promote members to admin, or demote them back to regular members.

    Args:
        group_jid: Group JID (id@g.us)
        participants: Phone numbers or JIDs
        admin: True promotes to admin, False demotes
    """
    return whatsapp_group_manage("promote" if admin else "demote", group_jid=group_jid, participants=participants)


@mcp.tool()
def rename_group(group_jid: str, name: str) -> dict[str, Any]:
    """Rename a group or community (at most 25 characters).

    Args:
        group_jid: Group JID (id@g.us)
        name: New name
    """
    return whatsapp_group_manage("rename", group_jid=group_jid, name=name)


@mcp.tool()
def set_group_description(group_jid: str, description: str) -> dict[str, Any]:
    """Set the description of a group or community. An empty string removes it.

    Args:
        group_jid: Group JID (id@g.us)
        description: New description, at most 2048 characters
    """
    return whatsapp_group_manage("set_description", group_jid=group_jid, description=description)


@mcp.tool()
def set_group_photo(
    group_jid: str,
    image_path: str | None = None,
    image_base64: str | None = None,
    image_url: str | None = None,
) -> dict[str, Any]:
    """Set the photo of a group or community. Pass exactly one image source; none removes the photo.

    This server runs on another machine than the caller, so a path on the caller's computer is
    not readable here. From a remote client use image_base64 (file bytes, base64) or image_url
    (public https link). image_path only works for files on the machine running this server.
    The image is center-cropped to a square and resized to 640x640 JPEG.

    Args:
        group_jid: Group JID (id@g.us)
        image_path: Path to an image on the machine running this server
        image_base64: The image file's bytes, base64-encoded (png, jpg or webp, up to 10 MB)
        image_url: Public https URL of the image
    """
    sources = [x for x in (image_path, image_base64, image_url) if x]
    if len(sources) > 1:
        return {"success": False, "message": "Pass only one of image_path, image_base64, image_url"}
    staged = image_path or None
    try:
        if image_base64:
            import base64
            import binascii

            raw = image_base64.split(",", 1)[1] if image_base64.startswith("data:") else image_base64
            try:
                data = base64.b64decode(raw, validate=False)
            except binascii.Error as e:
                return {"success": False, "message": f"image_base64 is not valid base64: {e}"}
            staged = whatsapp_stage_image_bytes(data)
        elif image_url:
            staged = whatsapp_stage_image_bytes(whatsapp_fetch_image_url(image_url))
    except (ValueError, requests.RequestException, OSError) as e:
        return {"success": False, "message": f"Could not get the image: {e}"}
    try:
        return whatsapp_group_manage("set_photo", group_jid=group_jid, media_path=staged)
    finally:
        if staged and staged != image_path:
            try:
                os.remove(staged)
            except OSError:
                pass


@mcp.tool()
def set_group_settings(
    group_jid: str,
    announce_only: bool | None = None,
    admins_edit_info_only: bool | None = None,
    require_join_approval: bool | None = None,
    who_can_add_members: str | None = None,
) -> dict[str, Any]:
    """Change group settings; only the ones you pass are changed.

    Args:
        group_jid: Group JID (id@g.us)
        announce_only: True = only admins can send messages
        admins_edit_info_only: True = only admins can edit name, photo and description
        require_join_approval: True = new members need admin approval
        who_can_add_members: "admin" or "all"
    """
    done: list[dict[str, Any]] = []
    steps: list[tuple[str, dict[str, Any]]] = []
    if announce_only is not None:
        steps.append(("set_announce", {"value": announce_only}))
    if admins_edit_info_only is not None:
        steps.append(("set_locked", {"value": admins_edit_info_only}))
    if require_join_approval is not None:
        steps.append(("set_join_approval", {"value": require_join_approval}))
    if who_can_add_members is not None:
        steps.append(("set_member_add_mode", {"mode": who_can_add_members}))
    if not steps:
        return {"success": False, "message": "No setting was given"}
    for action, extra in steps:
        result = whatsapp_group_manage(action, group_jid=group_jid, **extra)
        done.append({"action": action, "success": result.get("success", False), "message": result.get("message")})
        if not result.get("success"):
            break
    return {"success": all(d["success"] for d in done) and len(done) == len(steps), "steps": done}


@mcp.tool()
def get_group_invite_link(group_jid: str, reset: bool = False) -> dict[str, Any]:
    """Get the invite link of a group. reset=True revokes the old link and creates a new one.

    Args:
        group_jid: Group JID (id@g.us)
        reset: Revoke the current link first (anyone holding the old one loses access)
    """
    return whatsapp_group_manage("invite_link", group_jid=group_jid, reset=reset)


@mcp.tool()
def list_group_join_requests(group_jid: str) -> dict[str, Any]:
    """List people waiting for approval to join a group (groups with join approval on).

    Args:
        group_jid: Group JID (id@g.us)
    """
    return whatsapp_group_manage("list_requests", group_jid=group_jid)


@mcp.tool()
def answer_group_join_requests(group_jid: str, participants: list[str], approve: bool = True) -> dict[str, Any]:
    """Approve or reject pending join requests.

    Args:
        group_jid: Group JID (id@g.us)
        participants: JIDs from list_group_join_requests
        approve: True approves, False rejects
    """
    return whatsapp_group_manage(
        "approve_requests" if approve else "reject_requests", group_jid=group_jid, participants=participants
    )


@mcp.tool()
def preview_group_invite(invite_link: str) -> dict[str, Any]:
    """Read a group's name and size from an invite link without joining.

    Args:
        invite_link: https://chat.whatsapp.com/CODE or just the code
    """
    return whatsapp_group_manage("preview_link", code=invite_link)


@mcp.tool()
def join_group(invite_link: str) -> dict[str, Any]:
    """Join a group through an invite link. Only when the user asked to join that group.

    Args:
        invite_link: https://chat.whatsapp.com/CODE or just the code
    """
    return whatsapp_group_manage("join_link", code=invite_link)


@mcp.tool()
def leave_group(group_jid: str) -> dict[str, Any]:
    """Leave a group or community. Hard to undo (needs a new invite), so only on explicit request.

    Args:
        group_jid: Group JID (id@g.us)
    """
    return whatsapp_group_manage("leave", group_jid=group_jid)


@mcp.tool()
def get_profile_picture(jid: str, preview: bool = False, save_to: str | None = None) -> dict[str, Any]:
    """Download the profile picture of a WhatsApp contact or group.

    Args:
        jid: Contact JID (number@s.whatsapp.net or number@lid) or group JID (id@g.us)
        preview: Fetch the small thumbnail instead of the full-size image
        save_to: Optional destination file path to copy the picture to (e.g. ~/deck/foto.jpg)

    Returns:
        success, status (ok, no_picture, private, rate_limited, not_connected, invalid_jid, error),
        a message and, on success, the local file_path. Contacts can hide their photo
        (status "private") or have none (status "no_picture").
    """
    return whatsapp_get_profile_picture(jid, preview, save_to)


@mcp.tool()
def get_group_profile_pictures(group_jid: str, dest_dir: str | None = None, preview: bool = False) -> dict[str, Any]:
    """Download the profile picture of every member of a WhatsApp group.

    Args:
        group_jid: Group JID (id@g.us)
        dest_dir: Optional directory to copy the pictures to, named <name-slug>.jpg
        preview: Fetch small thumbnails instead of full-size images

    Returns:
        group_name, counts and one entry per member with name, jid, phone_jid, status
        (ok, no_picture, private, rate_limited, ...) and file_path, so members without a
        photo are easy to spot.
    """
    return whatsapp_get_group_profile_pictures(group_jid, dest_dir, preview)


@mcp.tool()
def download_media(message_id: str, chat_jid: str) -> dict[str, Any]:
    """Download media from a WhatsApp message and get the local file path.

    Args:
        message_id: The ID of the message containing the media
        chat_jid: The JID of the chat containing the message

    Returns:
        A dictionary containing success status, a status message, and the file path if successful
    """
    file_path = whatsapp_download_media(message_id, chat_jid)

    if file_path:
        return {"success": True, "message": "Media downloaded successfully", "file_path": file_path}
    else:
        return {"success": False, "message": "Failed to download media"}


@mcp.tool()
def view_media(
    message_id: str,
    chat_jid: str,
    max_dimension: PreviewDimension = media_preview.DEFAULT_MAX_DIMENSION,
) -> Any:
    """View the media of a WhatsApp message as an image.

    download_media only returns a local file path, which a client without
    filesystem access cannot open. This returns the picture itself instead.
    Videos return their first frame, which is enough to tell what was sent.
    Both are downscaled so a single photo cannot flood the context. Voice notes
    are not images — use transcribe_audio or read the transcript from the
    message content with list_messages.

    Args:
        message_id: The ID of the message containing the media
        chat_jid: The JID of the chat containing the message
        max_dimension: Longest edge of the returned image in pixels (default 1024)

    Returns:
        Image content on success, otherwise a dictionary explaining why not
    """
    try:
        media_preview.validate_max_dimension(max_dimension)
    except media_preview.PreviewError as exc:
        return {"success": False, "message": str(exc)}

    file_path = whatsapp_download_media(message_id, chat_jid)
    if not file_path:
        return {"success": False, "message": "Failed to download media"}

    with tempfile.TemporaryDirectory() as work_dir:
        try:
            data, image_format = media_preview.render_preview(file_path, max_dimension=max_dimension, work_dir=work_dir)
        except media_preview.PreviewError as exc:
            return {"success": False, "message": str(exc), "file_path": file_path}

    return Image(data=data, format=image_format)


@mcp.tool()
def transcribe_audio(message_id: str, chat_jid: str, force: bool = False) -> dict[str, Any]:
    """Transcribe a WhatsApp voice note and return its text.

    Runs whisper.cpp locally by default, or sends audio to the operator's
    configured OpenAI-compatible endpoint. The transcript is also written into
    the message's empty content field, so afterwards it is readable through list_messages by any
    client — including one with no filesystem access — without transcribing
    again.

    Call this for a voice note whose content field is still empty. Requires
    whisper.cpp, FFmpeg, and WHISPER_MODEL for the default provider; alternatively
    configure WHATSAPP_TRANSCRIPTION_PROVIDER=openai_compatible, URL and MODEL.

    Args:
        message_id: The ID of the message containing the voice note
        chat_jid: The JID of the chat containing the message
        force: Transcribe again even when a transcript is already stored

    Returns:
        A dictionary with success status and the transcript
    """
    if not force:
        existing = transcription.stored_transcript(MESSAGES_DB_PATH, message_id, chat_jid)
        if existing:
            return {"success": True, "message": "Transcript already stored", "transcript": existing}

    # WhatsApp expires media server-side after a few weeks, so the file has to
    # be on disk. This is a no-op when the bridge already downloaded it.
    file_path = whatsapp_download_media(message_id, chat_jid)
    if not file_path:
        return {
            "success": False,
            "message": (
                "Could not obtain the audio file. WhatsApp expires media after a while, "
                "so an old voice note may no longer be downloadable."
            ),
        }
    if not transcription.is_audio(file_path):
        return {
            "success": False,
            "message": "This message is not audio. Use download_media instead.",
            "file_path": file_path,
        }

    try:
        provider = transcription.provider_name()
        model = transcription.configured_model(provider)
        with tempfile.TemporaryDirectory() as work_dir:
            text = transcription.transcribe_file(file_path, work_dir, model=model, provider=provider)
    except transcription.TranscriptionError as exc:
        return {"success": False, "message": str(exc)}

    if not transcription.store_transcript(MESSAGES_DB_PATH, message_id, chat_jid, text, model, provider=provider):
        # The words are worth returning even when the row could not be updated,
        # but say so: without the row, list_messages will not show them.
        return {
            "success": True,
            "message": "Transcribed, but the transcript could not be stored on the message row",
            "transcript": text,
        }

    return {
        "success": True,
        "message": "Transcribed",
        "transcript": f"{transcription.label(model, provider)}{text}",
    }


def shutdown_handler(signum, frame):
    """Handle shutdown signals gracefully to prevent zombie processes."""
    sys.exit(0)


if __name__ == "__main__":
    # Capture before any await — os.getppid() is dynamic.
    parent_pid = os.getppid()
    # Register signal handlers for clean shutdown
    signal.signal(signal.SIGINT, shutdown_handler)
    signal.signal(signal.SIGTERM, shutdown_handler)

    # Resolve the transport first: host/port are only used (and validated) for the
    # network transports, so a bad WHATSAPP_MCP_PORT can't break a stdio launch.
    # The localhost default keeps a remote server unreachable until explicitly opened up.
    try:
        transport = resolve_transport(os.getenv("WHATSAPP_MCP_TRANSPORT"))
        if transport != "stdio":
            mcp.settings.host = resolve_host(os.getenv("WHATSAPP_MCP_HOST"))
            mcp.settings.port = resolve_port(os.getenv("WHATSAPP_MCP_PORT"))
            # stdout is reserved for the protocol on stdio; log startup to stderr.
            print(
                f"WhatsApp MCP server listening on {mcp.settings.host}:{mcp.settings.port} via {transport}",
                file=sys.stderr,
            )
    except ValueError as exc:
        raise SystemExit(str(exc)) from None

    if transport == "stdio":
        install_stdio_parent_watchdog("WHATSAPP_PARENT_WATCHDOG_S", parent_pid=parent_pid)
    mcp.run(transport=transport)
