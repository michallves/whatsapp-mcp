import pytest

import main
import whatsapp

PNG = b"\x89PNG\r\n\x1a\n" + b"0" * 20


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
    out = main.set_group_photo("1@g.us", image_base64=base64.b64encode(PNG).decode())
    assert out["success"] and seen["bytes"] == PNG
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


@pytest.mark.parametrize(
    ("data", "kind"),
    [
        (PNG, "png"),
        (b"\xff\xd8\xff\xe0abc", "jpeg"),
        (b"RIFF\x00\x00\x00\x00WEBPVP8 ", "webp"),
        (b"GIF89a", None),
        (b"%PDF-1.4", None),
    ],
)
def test_image_kind(data, kind):
    assert whatsapp.image_kind(data) == kind


def test_stage_image_bytes_validates_type_and_size(tmp_path, monkeypatch):
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(tmp_path / "outbox"))
    with pytest.raises(ValueError, match="png, jpeg or webp"):
        whatsapp.stage_image_bytes(b"GIF89a....")
    with pytest.raises(ValueError, match="5 MB"):
        whatsapp.stage_image_bytes(PNG + b"0" * (5 * 1024 * 1024))
    with pytest.raises(ValueError, match="empty"):
        whatsapp.stage_image_bytes(b"")
    assert whatsapp.stage_image_bytes(PNG).endswith(".png")


def test_set_group_photo_base64_with_wrong_type_is_rejected(monkeypatch):
    import base64

    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    out = main.set_group_photo("1@g.us", image_base64=base64.b64encode(b"GIF89a....").decode())
    assert out["success"] is False and "png, jpeg or webp" in out["message"] and calls == []


class _Resp:
    def __init__(self, status=200, headers=None, body=PNG):
        self.status_code, self.headers, self._body = status, headers or {}, body
        self.is_redirect = status in (301, 302, 303, 307, 308)

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False

    def raise_for_status(self):
        if self.status_code >= 400:
            raise whatsapp.requests.HTTPError(str(self.status_code))

    def iter_content(self, n):
        yield self._body


def test_image_url_follows_redirects_and_rechecks_each_hop(monkeypatch):
    monkeypatch.setattr(whatsapp, "_check_public_https", lambda url: None)
    hops = iter([_Resp(302, {"Location": "/b.png"}), _Resp(200)])
    monkeypatch.setattr(whatsapp.requests, "get", lambda *a, **k: next(hops))
    assert whatsapp._fetch_image_url("https://example.com/a.png") == PNG


def test_image_url_redirect_to_http_is_refused(monkeypatch):
    monkeypatch.setattr(whatsapp, "_check_public_https", whatsapp._check_public_https)
    monkeypatch.setattr(
        whatsapp.socket if hasattr(whatsapp, "socket") else __import__("socket"),
        "getaddrinfo",
        lambda *a, **k: [(0, 0, 0, "", ("93.184.216.34", 443))],
    )
    monkeypatch.setattr(whatsapp.requests, "get", lambda *a, **k: _Resp(302, {"Location": "http://example.com/x.png"}))
    with pytest.raises(ValueError, match="https"):
        whatsapp._fetch_image_url("https://example.com/a.png")


def test_image_url_too_many_redirects(monkeypatch):
    monkeypatch.setattr(whatsapp, "_check_public_https", lambda url: None)
    monkeypatch.setattr(whatsapp.requests, "get", lambda *a, **k: _Resp(302, {"Location": "https://example.com/x"}))
    with pytest.raises(ValueError, match="redirects"):
        whatsapp._fetch_image_url("https://example.com/a.png")


def test_image_url_declared_size_too_big(monkeypatch):
    monkeypatch.setattr(whatsapp, "_check_public_https", lambda url: None)
    monkeypatch.setattr(whatsapp.requests, "get", lambda *a, **k: _Resp(200, {"Content-Length": str(6 * 1024 * 1024)}))
    with pytest.raises(ValueError, match="5 MB"):
        whatsapp._fetch_image_url("https://example.com/a.png")


@pytest.mark.parametrize("path", ["C:\\Users\\Alma\\foto.jpg", "C:/Users/Alma/foto.jpg", "\\\\pc\\share\\f.jpg"])
def test_save_to_windows_path_is_refused(monkeypatch, path):
    calls = _fake_bridge(monkeypatch, lambda p: (200, {"success": True}))
    out = whatsapp.get_profile_picture("5585@s.whatsapp.net", save_to=path)
    assert out["success"] is False and "machine running this MCP server" in out["message"] and calls == []
    out = whatsapp.get_group_profile_pictures("1@g.us", dest_dir=path)
    assert out["success"] is False and calls == []
