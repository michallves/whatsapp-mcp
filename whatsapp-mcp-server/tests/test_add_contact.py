import sqlite3

import pytest

import whatsapp

VCARD = "BEGIN:VCARD\nFN:Romario\nTEL;waid=5585999990000:+55 85 99999-0000\nEND:VCARD"


@pytest.fixture
def bridge(monkeypatch, tmp_path):
    monkeypatch.setattr(whatsapp, "ALIASES_PATH", str(tmp_path / "contact_aliases.json"))
    monkeypatch.setattr(whatsapp, "_resolve_lid_to_phone", lambda _lid: None)
    state = {
        "registered": {"5585999990000"},
        "edit_error": None,
        "calls": [],
        "lookup_jid": lambda p: f"{p}@s.whatsapp.net",
    }

    def fake(endpoint, payload, timeout=60):
        state["calls"].append((endpoint, payload))
        if endpoint == "check-numbers":
            results = [
                {
                    "phone": p,
                    "is_registered": p in state["registered"],
                    **({"jid": state["lookup_jid"](p)} if p in state["registered"] else {}),
                }
                for p in payload["phones"]
            ]
            return 200, {"success": True, "message": "ok", "results": results}
        if state["edit_error"]:
            return 502, {"success": False, "message": state["edit_error"]}
        return 200, {"success": True, "message": "Contact saved"}

    monkeypatch.setattr(whatsapp, "_bridge_post_json", fake)
    return state


@pytest.fixture
def messages_db(monkeypatch, tmp_path):
    path = tmp_path / "messages.db"
    conn = sqlite3.connect(path)
    conn.execute("CREATE TABLE messages (id TEXT, chat_jid TEXT, content TEXT, deleted_at TIMESTAMP)")
    conn.execute("INSERT INTO messages VALUES ('m1', 'c@s.whatsapp.net', ?, NULL)", (VCARD,))
    conn.execute("INSERT INTO messages VALUES ('m2', 'c@s.whatsapp.net', 'plain text', NULL)")
    conn.execute("INSERT INTO messages VALUES ('m3', 'c@s.whatsapp.net', ?, '2026-01-01')", (VCARD,))
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(path))


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("+55 (85) 99999-0000", "5585999990000"),
        ("85999990000", "5585999990000"),
        ("8533334444", "558533334444"),
        ("005585999990000", "5585999990000"),
        ("5585999990000", "5585999990000"),
        ("+1 202 555 0100", "12025550100"),
        ("123", None),
        ("abc", None),
        ("", None),
    ],
)
def test_normalize_phone_number(raw, expected):
    assert whatsapp.normalize_phone_number(raw) == expected


def test_add_by_number_saves_contact_and_alias(bridge):
    result = whatsapp.add_contact(name="Romario - Fortes - PO", phone="85 99999-0000")
    assert result["success"]
    assert result["saved"] == [
        {"name": "Romario - Fortes - PO", "phone": "5585999990000", "jid": "5585999990000@s.whatsapp.net"}
    ]
    assert bridge["calls"][-1] == (
        "contact-edit",
        {"jid": "5585999990000@s.whatsapp.net", "full_name": "Romario - Fortes - PO"},
    )
    assert whatsapp.get_alias("5585999990000@s.whatsapp.net") == "Romario - Fortes - PO"


def test_lid_lookup_still_saves_with_phone_jid(bridge):
    bridge["lookup_jid"] = lambda _phone: "1234567890@lid"
    result = whatsapp.add_contact(name="Maria", phone="85999990000")
    assert result["saved"][0]["jid"] == "5585999990000@s.whatsapp.net"
    assert bridge["calls"][-1][1]["jid"] == "5585999990000@s.whatsapp.net"
    assert whatsapp.get_alias("5585999990000@s.whatsapp.net") == "Maria"


def test_add_by_number_requires_name_and_valid_phone(bridge):
    assert not whatsapp.add_contact(phone="85999990000")["success"]
    assert not whatsapp.add_contact(name="X", phone="12")["success"]
    assert bridge["calls"] == []


def test_number_not_on_whatsapp_is_reported_and_not_saved(bridge):
    bridge["registered"] = set()
    result = whatsapp.add_contact(name="Ghost", phone="85999990000")
    assert not result["success"]
    assert result["failed"] == [{"name": "Ghost", "phone": "5585999990000", "reason": "number is not on WhatsApp"}]
    assert [endpoint for endpoint, _ in bridge["calls"]] == ["check-numbers"]
    assert whatsapp.get_alias("5585999990000@s.whatsapp.net") is None


def test_bridge_edit_error_is_reported_and_alias_not_stored(bridge):
    bridge["edit_error"] = "WhatsApp client is not connected."
    result = whatsapp.add_contact(name="Romario", phone="85999990000")
    assert not result["success"]
    assert result["failed"][0]["reason"] == "WhatsApp client is not connected."
    assert whatsapp.get_alias("5585999990000@s.whatsapp.net") is None


def test_add_from_contact_card_uses_card_name(bridge, messages_db):
    result = whatsapp.add_contact(message_id="m1", chat_jid="c@s.whatsapp.net")
    assert result["success"]
    assert result["saved"][0]["name"] == "Romario"


def test_card_name_can_be_overridden_when_single_number(bridge, messages_db):
    result = whatsapp.add_contact(name="Romario - PO", message_id="m1", chat_jid="c@s.whatsapp.net")
    assert result["saved"][0]["name"] == "Romario - PO"


def test_message_without_card_or_missing_or_deleted(bridge, messages_db):
    for message_id in ("m2", "nope", "m3"):
        assert not whatsapp.add_contact(message_id=message_id, chat_jid="c@s.whatsapp.net")["success"]
    assert bridge["calls"] == []


def test_requires_phone_or_message(bridge):
    assert not whatsapp.add_contact()["success"]
    assert not whatsapp.add_contact(message_id="m1")["success"]


def test_multiple_numbers_in_card_reject_name_override(bridge, messages_db, monkeypatch):
    two_numbers = (
        "BEGIN:VCARD\nFN:Duo\nTEL;waid=5585911110000:+5585911110000\nTEL;waid=5585922220000:+5585922220000\nEND:VCARD"
    )
    monkeypatch.setattr(whatsapp, "_read_message_content", lambda *_: two_numbers)
    assert not whatsapp.add_contact(name="Other", message_id="m1", chat_jid="c")["success"]
    bridge["registered"] = {"5585911110000", "5585922220000"}
    result = whatsapp.add_contact(message_id="m1", chat_jid="c")
    assert result["success"] and len(result["saved"]) == 2
