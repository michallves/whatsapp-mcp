import os

# Keep timestamp assertions independent of the machine's local timezone.
os.environ["WHATSAPP_TIMEZONE"] = "UTC"
