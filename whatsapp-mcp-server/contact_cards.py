import re
from dataclasses import dataclass

_VCARD_BLOCK = re.compile(r"BEGIN:VCARD(.*?)END:VCARD", re.DOTALL | re.IGNORECASE)
_LINE_FOLD = re.compile(r"\r?\n[ \t]")
_WAID_PARAM = re.compile(r"(?:^|;)waid=(\d+)", re.IGNORECASE)


@dataclass(frozen=True)
class ContactCard:
    name: str
    phones: tuple[str, ...]


def _property_name(raw_key: str) -> str:
    name = raw_key.split(";", 1)[0]
    return name.rsplit(".", 1)[-1].upper()


def _name_from_structured(value: str) -> str:
    family, given, *_ = [*value.split(";"), "", ""]
    return " ".join(part.strip() for part in (given, family) if part.strip())


def _phone_from_property(raw_key: str, value: str) -> str:
    waid = _WAID_PARAM.search(raw_key)
    if waid:
        return waid.group(1)
    digits = "".join(ch for ch in value if ch.isdigit())
    return f"+{digits}" if value.strip().startswith("+") else digits


def _parse_card(block: str) -> ContactCard:
    full_name = ""
    structured_name = ""
    phones: list[str] = []
    for line in _LINE_FOLD.sub("", block).splitlines():
        raw_key, separator, value = line.partition(":")
        if not separator:
            continue
        property_name = _property_name(raw_key)
        if property_name == "FN":
            full_name = value.strip()
        elif property_name == "N":
            structured_name = _name_from_structured(value)
        elif property_name == "TEL":
            phone = _phone_from_property(raw_key, value)
            if phone and phone not in phones:
                phones.append(phone)
    return ContactCard(name=full_name or structured_name, phones=tuple(phones))


def parse_vcards(text: str) -> list[ContactCard]:
    return [_parse_card(match.group(1)) for match in _VCARD_BLOCK.finditer(text or "")]
