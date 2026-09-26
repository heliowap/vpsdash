#!/usr/bin/env python3
"""Render the committed GitHub App manifest as a browser registration form."""

import html
import json
from pathlib import Path

root = Path(__file__).resolve().parent.parent
manifest = json.loads((root / "app-manifest.json").read_text())
value = html.escape(json.dumps(manifest, separators=(",", ":")), quote=True)
print("<!doctype html><html lang='pt-BR'><meta charset='utf-8'>")
print("<title>Registrar gh-agents-ops</title><body>")
print("<h1>Registrar gh-agents-ops</h1>")
print("<p>Revise as permissões no GitHub antes de criar o App.</p>")
print("<form action='https://github.com/settings/apps/new' method='post'>")
print(f"<input type='hidden' name='manifest' value='{value}'>")
print("<button type='submit'>Continuar no GitHub</button></form></body></html>")
