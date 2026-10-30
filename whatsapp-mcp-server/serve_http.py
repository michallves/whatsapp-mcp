"""Serve o MCP do WhatsApp em HTTP, só acessível com o token no caminho da URL.

Uso: WHATSAPP_MCP_HTTP_TOKEN_FILE=... python serve_http.py   (escuta em 127.0.0.1:8201)
URL final: https://<host-do-tunel>/<token>/mcp
"""

import hmac
import os

import uvicorn
from mcp.server.transport_security import TransportSecuritySettings

import main  # registra as ferramentas em main.mcp

TOKEN = open(os.environ["WHATSAPP_MCP_HTTP_TOKEN_FILE"]).read().strip()
assert len(TOKEN) >= 32, "token curto demais"
PREFIX = "/" + TOKEN

# O host vem do túnel (não é localhost); a proteção é o token no caminho.
main.mcp.settings.transport_security = TransportSecuritySettings(enable_dns_rebinding_protection=False)
inner = main.mcp.streamable_http_app()


class TokenGuard:
    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] == "lifespan":
            return await self.app(scope, receive, send)
        if scope["type"] == "http":
            path = scope["path"]
            seg = path[: len(PREFIX)]
            rest = path[len(PREFIX) :]
            if hmac.compare_digest(seg, PREFIX) and (rest == "" or rest.startswith("/")):
                scope = dict(scope, path=rest or "/", raw_path=(rest or "/").encode())
                return await self.app(scope, receive, send)
        await send({"type": "http.response.start", "status": 404, "headers": [(b"content-type", b"text/plain")]})
        await send({"type": "http.response.body", "body": b"not found"})


if __name__ == "__main__":
    uvicorn.run(TokenGuard(inner), host="127.0.0.1", port=int(os.environ.get("PORT", "8201")), log_level="warning")
