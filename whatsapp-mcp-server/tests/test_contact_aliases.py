import whatsapp


def _use_tmp_aliases(tmp_path, monkeypatch):
    monkeypatch.setattr(whatsapp, "ALIASES_PATH", str(tmp_path / "contact_aliases.json"))
    monkeypatch.setattr(whatsapp, "_resolve_lid_to_phone", lambda _lid: None)


def test_alias_roundtrip_and_removal(tmp_path, monkeypatch):
    _use_tmp_aliases(tmp_path, monkeypatch)
    result = whatsapp.set_contact_alias("5585999990000@s.whatsapp.net", "Maria Souza - EyeMed - Requisitos")
    assert result["success"] and result["phone"] == "5585999990000"
    assert whatsapp.get_alias("+55 85 99999-0000") == "Maria Souza - EyeMed - Requisitos"
    assert whatsapp.list_contact_aliases() == {"5585999990000": "Maria Souza - EyeMed - Requisitos"}

    removed = whatsapp.set_contact_alias("5585999990000@s.whatsapp.net", "  ")
    assert removed["success"] and removed["alias"] is None
    assert whatsapp.get_alias("5585999990000@s.whatsapp.net") is None


def test_alias_rejects_jid_without_digits(tmp_path, monkeypatch):
    _use_tmp_aliases(tmp_path, monkeypatch)
    assert not whatsapp.set_contact_alias("", "X")["success"]


def test_lid_alias_resolves_through_phone(tmp_path, monkeypatch):
    _use_tmp_aliases(tmp_path, monkeypatch)
    monkeypatch.setattr(
        whatsapp, "_resolve_lid_to_phone", lambda lid: "5585999990000" if lid == "123456789012345" else None
    )
    whatsapp.set_contact_alias("5585999990000@s.whatsapp.net", "Maria - EyeMed")
    assert whatsapp.get_alias("123456789012345@lid") == "Maria - EyeMed"


def test_alias_overrides_names_in_outputs(tmp_path, monkeypatch):
    _use_tmp_aliases(tmp_path, monkeypatch)
    whatsapp.set_contact_alias("5585999990000@s.whatsapp.net", "Maria - EyeMed")
    assert whatsapp.get_sender_name("5585999990000@s.whatsapp.net") == "Maria - EyeMed"
    contact = whatsapp.Contact(phone_number="5585999990000", name="maria darm", jid="5585999990000@s.whatsapp.net")
    assert whatsapp.contact_to_dict(contact)["name"] == "Maria - EyeMed"
    dm = whatsapp.Chat(jid="5585999990000@s.whatsapp.net", name="maria darm", last_message_time=None)
    assert whatsapp.chat_to_dict(dm)["name"] == "Maria - EyeMed"
    group = whatsapp.Chat(jid="120363012345678901@g.us", name="Time", last_message_time=None)
    assert whatsapp.chat_to_dict(group)["name"] == "Time"


def test_search_finds_contacts_by_alias_text(tmp_path, monkeypatch):
    _use_tmp_aliases(tmp_path, monkeypatch)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(tmp_path / "missing.db"))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(tmp_path / "missing2.db"))
    whatsapp.set_contact_alias("5585999990000@s.whatsapp.net", "Maria - EyeMed - Requisitos")
    found = whatsapp.search_contacts("eyemed")
    assert [c["phone_number"] for c in found] == ["5585999990000"]
