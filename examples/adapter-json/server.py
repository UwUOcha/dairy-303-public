#!/usr/bin/env python3
"""Reference adapter. Replace read_data() with your university's data acquisition."""
import datetime as dt
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit


def read_data():
    return json.loads(Path(os.getenv("ADAPTER_DATA", str(Path(__file__).with_name("data.json")))).read_text())


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        path = urlsplit(self.path)
        if path.path == "/health":
            return self.reply(200, {"status": "ok"})
        token = os.getenv("ADAPTER_TOKEN", "")
        if token and not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + token):
            return self.error(401, "unauthorized")
        try:
            data = read_data()
            if path.path == "/v1/info":
                return self.reply(200, {"version": "1", "source": data["source"], "timezone": data["timezone"], "staff_directory": True})
            if path.path == "/v1/catalog":
                return self.reply(200, {"complete": True, "fetched_at": now(), "departments": data["departments"], "groups": data["groups"]})
            if path.path == "/v1/teachers":
                return self.reply(200, {"complete": True, "fetched_at": now(), "teachers": data["teachers"]})
            if path.path == "/v1/schedule":
                query = parse_qs(path.query, strict_parsing=True)
                if set(query) != {"group", "from", "to"} or any(len(v) != 1 for v in query.values()):
                    return self.error(400, "invalid_request")
                group, first, last = (query[k][0] for k in ("group", "from", "to"))
                start, end = dt.date.fromisoformat(first), dt.date.fromisoformat(last)
                if not 0 <= (end - start).days <= 30:
                    return self.error(400, "invalid_request")
                if group not in {g["id"] for g in data["groups"]}:
                    return self.error(404, "not_found")
                lessons = []
                for day_offset in range((end - start).days + 1):
                    day = start + dt.timedelta(days=day_offset)
                    for item in data["weekly_lessons"]:
                        if item["group_id"] == group and item["weekday"] == day.isoweekday():
                            lesson = {k: v for k, v in item.items() if k not in {"weekday", "group_id"}}
                            lesson.update(id=item["id"] + ":" + day.isoformat(), date=day.isoformat(), anchor_group_id=group)
                            lessons.append(lesson)
                return self.reply(200, {"group_id": group, "from": first, "to": last, "complete": True, "status": "published", "fetched_at": now(), "lessons": lessons, "subgroups": data.get("subgroups", {}).get(group, []), "workdays": [1, 2, 3, 4, 5]})
            return self.error(404, "not_found")
        except (ValueError, KeyError):
            return self.error(400, "invalid_request")
        except OSError:
            return self.error(503, "unavailable")

    def error(self, status, code):
        self.reply(status, {"code": code, "message": code})

    def reply(self, status, data):
        body = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    ThreadingHTTPServer((os.getenv("ADAPTER_HOST", "127.0.0.1"), int(os.getenv("ADAPTER_PORT", "8310"))), Handler).serve_forever()
