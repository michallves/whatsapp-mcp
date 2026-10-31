"""send_audio_message converts non-.ogg input to a temp file that must be
removed after the bridge call, on both success and failure paths."""

import os

import pytest

import whatsapp


class _Resp:
    def __init__(self, status_code=200, payload=None, text=""):
        self.status_code = status_code
        self._payload = payload or {"success": True, "message": "ok"}
        self.text = text

    def json(self):
        return self._payload


@pytest.mark.parametrize(("status_code", "expected_success"), [(200, True), (500, False)])
def test_send_audio_removes_converted_temp_file(tmp_path, monkeypatch, status_code, expected_success):
    src = tmp_path / "voice.mp3"
    src.write_bytes(b"not really mp3")
    converted = tmp_path / "converted.ogg"

    def fake_convert(path):
        converted.write_bytes(b"ogg")
        return str(converted)

    monkeypatch.setattr(whatsapp.audio, "convert_to_opus_ogg_temp", fake_convert)
    monkeypatch.setattr(whatsapp.requests, "post", lambda *a, **k: _Resp(status_code=status_code))

    success, _ = whatsapp.send_audio_message("12025551234", str(src))

    assert success is expected_success
    assert not os.path.exists(converted)
    assert src.exists()  # the caller's original file is never touched


def test_send_audio_removes_converted_temp_file_after_request_exception(tmp_path, monkeypatch):
    src = tmp_path / "voice.mp3"
    src.write_bytes(b"not really mp3")
    converted = tmp_path / "converted.ogg"

    def fake_convert(path):
        converted.write_bytes(b"ogg")
        return str(converted)

    def raise_request_exception(*args, **kwargs):
        raise whatsapp.requests.RequestException("bridge unavailable")

    monkeypatch.setattr(whatsapp.audio, "convert_to_opus_ogg_temp", fake_convert)
    monkeypatch.setattr(whatsapp.requests, "post", raise_request_exception)

    success, message = whatsapp.send_audio_message("12025551234", str(src))

    assert success is False
    assert message == "Request error: bridge unavailable"
    assert not os.path.exists(converted)
    assert src.exists()  # the caller's original file is never touched


def test_send_audio_leaves_native_ogg_in_place(tmp_path, monkeypatch):
    src = tmp_path / "voice.ogg"
    src.write_bytes(b"ogg")
    monkeypatch.setattr(whatsapp.audio, "convert_to_opus_ogg_temp", lambda p: pytest.fail("must not convert .ogg"))
    monkeypatch.setattr(whatsapp.requests, "post", lambda *a, **k: _Resp())

    ok, _ = whatsapp.send_audio_message("12025551234", str(src))

    assert ok is True
    assert src.exists()


def _fake_ffmpeg(cmd, **kwargs):
    with open(cmd[-1], "wb") as out:
        out.write(b"ogg")


def test_send_audio_converts_inside_source_dir_and_leaves_nothing(tmp_path, monkeypatch):
    """The bridge only reads its media roots, so the .ogg must not land in /tmp."""
    outbox = tmp_path / "outbox"
    outbox.mkdir()
    src = outbox / "voice.mp3"
    src.write_bytes(b"mp3")
    sent = {}

    def fake_post(url, json, headers):
        sent["path"] = json["media_path"]
        sent["existed"] = os.path.exists(json["media_path"])
        return _Resp()

    monkeypatch.setattr(whatsapp.audio.subprocess, "run", _fake_ffmpeg)
    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    ok, _ = whatsapp.send_audio_message("12025551234", str(src))

    assert ok is True
    assert os.path.dirname(sent["path"]) == os.path.realpath(outbox)
    assert sent["path"].endswith(".ogg")
    assert sent["existed"] is True
    assert [p.name for p in outbox.iterdir()] == ["voice.mp3"]


def test_convert_of_symlink_does_not_write_into_the_links_directory(tmp_path, monkeypatch):
    """A link in the outbox must not launder its target past the bridge's guard."""
    outbox = tmp_path / "outbox"
    elsewhere = tmp_path / "elsewhere"
    outbox.mkdir()
    elsewhere.mkdir()
    target = elsewhere / "secret.mp3"
    target.write_bytes(b"mp3")
    link = outbox / "voice.mp3"
    link.symlink_to(target)

    monkeypatch.setattr(whatsapp.audio.subprocess, "run", _fake_ffmpeg)

    converted = whatsapp.audio.convert_to_opus_ogg_temp(str(link))

    assert os.path.dirname(converted) == os.path.realpath(elsewhere)
    assert [p.name for p in outbox.iterdir()] == ["voice.mp3"]


def test_failed_conversion_leaves_nothing_in_source_dir(tmp_path, monkeypatch):
    src = tmp_path / "voice.mp3"
    src.write_bytes(b"mp3")

    def failing_ffmpeg(cmd, **kwargs):
        raise whatsapp.audio.subprocess.CalledProcessError(1, cmd, stderr="boom")

    monkeypatch.setattr(whatsapp.audio.subprocess, "run", failing_ffmpeg)

    ok, message = whatsapp.send_audio_message("12025551234", str(src))

    assert ok is False
    assert "boom" in message
    assert [p.name for p in tmp_path.iterdir()] == ["voice.mp3"]
