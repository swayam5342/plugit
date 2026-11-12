"""A tiny stand-in for the Petstore API, so the demo runs offline."""
import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PETS = [
    {"id": 1, "name": "Rex", "status": "available", "category": {"id": 1, "name": "Dogs"}, "tags": [{"id": 1, "name": "friendly"}]},
    {"id": 2, "name": "Mittens", "status": "available", "category": {"id": 2, "name": "Cats"}, "tags": []},
    {"id": 3, "name": "Bubbles", "status": "sold", "category": {"id": 3, "name": "Fish"}, "tags": []},
]


class Handler(BaseHTTPRequestHandler):
    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path, _, query = self.path.partition("?")
        if path == "/api/v3/pet/findByStatus":
            status = "available"
            m = re.search(r"status=([a-z]+)", query)
            if m:
                status = m.group(1)
            return self.reply(200, [p for p in PETS if p["status"] == status])
        m = re.fullmatch(r"/api/v3/pet/(\d+)", path)
        if m:
            for p in PETS:
                if p["id"] == int(m.group(1)):
                    return self.reply(200, p)
            return self.reply(404, {"code": 404, "message": "Pet not found"})
        if path == "/api/v3/store/inventory":
            return self.reply(200, {"available": 2, "sold": 1})
        return self.reply(404, {"code": 404, "message": "not found"})

    def log_message(self, *args):
        pass


import sys
ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
