import whatsapp


def _fake_bridge(monkeypatch, responses):
    """Route _bridge_post_json to per-endpoint canned answers: {endpoint: callable(payload)}."""
    calls = []

    def fake(endpoint, payload, timeout=60):
        calls.append((endpoint, payload))
        return responses[endpoint](payload)

    monkeypatch.setattr(whatsapp, "_bridge_post_json", fake)
    monkeypatch.setattr(whatsapp.time, "sleep", lambda _s: None, raising=False)
    return calls


def test_get_profile_picture_copies_to_save_to(tmp_path, monkeypatch):
    src = tmp_path / "store" / "profile-pictures" / "a.jpg"
    src.parent.mkdir(parents=True)
    src.write_bytes(b"jpeg")
    _fake_bridge(
        monkeypatch,
        {"profile-picture": lambda p: (200, {"success": True, "status": "ok", "message": "m", "path": str(src)})},
    )

    out = tmp_path / "deck" / "foto.jpg"
    result = whatsapp.get_profile_picture("5585@s.whatsapp.net", save_to=str(out))

    assert result["success"] and result["file_path"] == str(out)
    assert out.read_bytes() == b"jpeg"


def test_get_profile_picture_reports_private_and_missing(monkeypatch):
    for status, code in (("private", 403), ("no_picture", 404), ("rate_limited", 429)):
        _fake_bridge(
            monkeypatch,
            {"profile-picture": lambda p, s=status, c=code: (c, {"success": False, "status": s, "message": s})},
        )
        result = whatsapp.get_profile_picture("5585@s.whatsapp.net")
        assert result == {"success": False, "status": status, "message": status}


def test_get_profile_picture_handles_plain_text_error(monkeypatch):
    _fake_bridge(monkeypatch, {"profile-picture": lambda p: (400, {"message": "jid is required"})})
    result = whatsapp.get_profile_picture("x")
    assert not result["success"] and result["message"] == "jid is required"


def test_slugify_filename():
    assert whatsapp._slugify_filename("João Silva") == "joao-silva"
    assert whatsapp._slugify_filename("Antônio Frota Monteiro") == "antonio-frota-monteiro"
    assert whatsapp._slugify_filename("🙂") == "contact"


def test_group_profile_pictures_names_files_and_lists_missing(tmp_path, monkeypatch):
    src = tmp_path / "src.jpg"
    src.write_bytes(b"jpeg")

    def participants(_p):
        return 200, {
            "success": True,
            "group_name": "Team group",
            "participants": [
                {"jid": "1@lid", "lid": "1@lid", "phone_jid": "5585111@s.whatsapp.net"},
                {"jid": "2@lid", "lid": "2@lid"},
                {"jid": "3@lid", "lid": "3@lid", "phone_jid": "5585333@s.whatsapp.net"},
            ],
        }

    def picture(payload):
        if payload["jid"] == "5585111@s.whatsapp.net":
            return 200, {"success": True, "status": "ok", "path": str(src)}
        if payload["jid"] == "2@lid":
            return 404, {"success": False, "status": "no_picture", "message": "none"}
        return 403, {"success": False, "status": "private", "message": "hidden"}

    calls = _fake_bridge(monkeypatch, {"group-participants": participants, "profile-picture": picture})
    names = {"5585111@s.whatsapp.net": "Rubens Silva", "2@lid": "Rafael Lima", "5585333@s.whatsapp.net": "Davi"}
    monkeypatch.setattr(whatsapp, "get_sender_name", lambda jid: names[jid])

    result = whatsapp.get_group_profile_pictures("120@g.us", dest_dir=str(tmp_path / "out"))

    assert result["group_name"] == "Team group" and result["total"] == 3 and result["downloaded"] == 1
    assert [r["status"] for r in result["results"]] == ["ok", "no_picture", "private"]
    assert (tmp_path / "out" / "rubens-silva.jpg").read_bytes() == b"jpeg"
    assert result["results"][1]["file_path"] is None
    # phone JID is preferred over the LID when both are known
    assert [c[1]["jid"] for c in calls if c[0] == "profile-picture"][0] == "5585111@s.whatsapp.net"


def test_group_profile_pictures_propagates_bridge_failure(monkeypatch):
    _fake_bridge(monkeypatch, {"group-participants": lambda p: (400, {"success": False, "message": "not a group"})})
    assert whatsapp.get_group_profile_pictures("x@s.whatsapp.net") == {"success": False, "message": "not a group"}
