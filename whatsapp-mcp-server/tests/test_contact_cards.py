from contact_cards import ContactCard, parse_vcards

WHATSAPP_CARD = (
    "BEGIN:VCARD\n"
    "VERSION:3.0\n"
    "N:;Romario Silva;;;\n"
    "FN:Romario Silva\n"
    "item1.TEL;waid=5585999990000:+55 85 99999-0000\n"
    "item1.X-ABLabel:Mobile\n"
    "END:VCARD"
)


def test_parses_name_and_waid_phone():
    assert parse_vcards(WHATSAPP_CARD) == [ContactCard(name="Romario Silva", phones=("5585999990000",))]


def test_falls_back_to_structured_name_and_plain_phone():
    card = "BEGIN:VCARD\nVERSION:3.0\nN:Souza;Maria;;;\nTEL;TYPE=CELL:(85) 98888-7777\nEND:VCARD"
    assert parse_vcards(card) == [ContactCard(name="Maria Souza", phones=("85988887777",))]


def test_keeps_plus_sign_without_waid_and_drops_duplicates():
    card = "BEGIN:VCARD\nFN:Ana\nTEL:+55 85 97777-0000\nTEL:+55 85 97777-0000\nTEL:+1 202 555 0100\nEND:VCARD"
    assert parse_vcards(card)[0].phones == ("+5585977770000", "+12025550100")


def test_unfolds_wrapped_lines():
    card = "BEGIN:VCARD\nFN:Nome Muito\n  Longo\nTEL;waid=5585911112222:+5585911112222\nEND:VCARD"
    assert parse_vcards(card)[0].name == "Nome Muito Longo"


def test_parses_several_cards():
    text = WHATSAPP_CARD + "\nBEGIN:VCARD\nFN:Bruno\nTEL;waid=5511900000000:+5511900000000\nEND:VCARD"
    assert [card.name for card in parse_vcards(text)] == ["Romario Silva", "Bruno"]


def test_ignores_text_without_cards():
    assert parse_vcards("just a message") == []
    assert parse_vcards("") == []
