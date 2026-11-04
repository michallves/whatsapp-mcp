import pytest

import main
import whatsapp


def _fake_bridge(monkeypatch, answer):
    calls = []

    def fake(endpoint, payload, timeout=60):
        calls.append((endpoint, payload))
        return answer(payload)

    monkeypatch.setattr(whatsapp, "_bridge_post_json", fake)
    return calls


def test_group_manage_drops_none_fields(monkeypatch):
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True, "message": "ok"}))
    out = whatsapp.group_manage("rename", group_jid="1@g.us", name="X", description=None)
    assert out["success"]
    assert calls == [("group-manage", {"action": "rename", "group_jid": "1@g.us", "name": "X"})]


def test_group_manage_reports_bridge_errors(monkeypatch):
    _fake_bridge(monkeypatch, lambda p: (400, {"message": "name is required"}))
    out = whatsapp.group_manage("create_group")
    assert out == {"success": False, "message": "name is required"}


def test_group_manage_unusable_body(monkeypatch):
    _fake_bridge(monkeypatch, lambda p: (502, {}))
    assert whatsapp.group_manage("info", group_jid="1@g.us")["success"] is False


def test_photo_inside_media_root_is_not_copied(tmp_path, monkeypatch):
    root = tmp_path / "outbox"
    root.mkdir()
    img = root / "a.png"
    img.write_bytes(b"x")
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(root))
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    whatsapp.group_manage("set_photo", group_jid="1@g.us", media_path=str(img))
    assert calls[0][1]["media_path"] == str(img.resolve())


def test_photo_outside_media_root_is_staged(tmp_path, monkeypatch):
    root = tmp_path / "outbox"
    src = tmp_path / "logo.png"
    src.write_bytes(b"png")
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(root))
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    whatsapp.group_manage("set_photo", group_jid="1@g.us", media_path=str(src))
    staged = calls[0][1]["media_path"]
    assert staged.startswith(str(root.resolve())) and open(staged, "rb").read() == b"png"
    assert src.exists()


def test_photo_missing_file(tmp_path, monkeypatch):
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(tmp_path / "outbox"))
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    out = whatsapp.group_manage("set_photo", group_jid="1@g.us", media_path=str(tmp_path / "nope.png"))
    assert out["success"] is False and calls == []


def test_create_group_tool_maps_arguments(monkeypatch):
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    main.create_group("Time", ["5585999990000"], community_jid="2@g.us", announce_only=True)
    payload = calls[0][1]
    assert payload["action"] == "create_group" and payload["parent_jid"] == "2@g.us" and payload["announce"] is True
    assert "locked" not in payload


def test_set_group_settings_stops_on_first_failure(monkeypatch):
    seen = []

    def answer(p):
        seen.append(p["action"])
        return (200, {"success": p["action"] != "set_locked", "message": "m"})

    _fake_bridge(monkeypatch, answer)
    out = main.set_group_settings("1@g.us", announce_only=True, admins_edit_info_only=True, require_join_approval=True)
    assert seen == ["set_announce", "set_locked"] and out["success"] is False


def test_set_group_settings_needs_a_setting():
    assert main.set_group_settings("1@g.us")["success"] is False


@pytest.mark.parametrize(("admin", "action"), [(True, "promote"), (False, "demote")])
def test_set_group_admins_actions(monkeypatch, admin, action):
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    main.set_group_admins("1@g.us", ["5585999990000"], admin=admin)
    assert calls[0][1]["action"] == action


def test_set_group_photo_from_base64_is_staged_and_cleaned(tmp_path, monkeypatch):
    import base64

    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(tmp_path / "outbox"))
    seen = {}

    def answer(p):
        seen["path"] = p["media_path"]
        seen["bytes"] = open(p["media_path"], "rb").read()
        return (200, {"success": True})

    _fake_bridge(monkeypatch, answer)
    out = main.set_group_photo("1@g.us", image_base64=base64.b64encode(b"imgdata").decode())
    assert out["success"] and seen["bytes"] == b"imgdata"
    assert not (tmp_path / "outbox").joinpath(seen["path"]).exists()


def test_set_group_photo_rejects_two_sources_and_bad_base64(monkeypatch):
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    assert main.set_group_photo("1@g.us", image_path="/a", image_url="https://x/y.png")["success"] is False
    assert main.set_group_photo("1@g.us", image_base64="!!!")["success"] is False
    assert calls == []


@pytest.mark.parametrize("url", ["http://example.com/a.png", "https://127.0.0.1/a.png", "https://localhost/a.png"])
def test_image_url_refuses_non_https_and_private_hosts(url):
    with pytest.raises(ValueError):
        whatsapp._fetch_image_url(url)
