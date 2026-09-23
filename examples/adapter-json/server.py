#!/usr/bin/env python3
"""Учебный адаптер. Для своего вуза перепишите функции из раздела «ВАША ЧАСТЬ»."""
import datetime as dt
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit


# ─── ВАША ЧАСТЬ ──────────────────────────────────────────────────────────────
# Здесь адаптер узнаёт расписание своего вуза. В примере оно читается из
# data.json; в настоящем адаптере здесь запросы к API вуза, разбор HTML или
# Excel. Остальной файл менять не нужно.

def read_data():
    return json.loads(Path(os.getenv("ADAPTER_DATA", str(Path(__file__).with_name("data.json")))).read_text())


def load_source():
    """Постоянное имя установки и часовой пояс вуза (IANA)."""
    data = read_data()
    return data["source"], data["timezone"]


def load_catalog():
    """Все факультеты и группы вуза: (departments, groups)."""
    data = read_data()
    return data["departments"], data["groups"]


def load_lessons(group, start, end):
    """Пары группы с даты start по end включительно и её подгруппы: (lessons, subgroups).

    Каждая пара — словарь с id, date, start, end, subject и необязательными
    kind, room, teachers, subgroup_id. id должен быть одинаковым при каждом
    запросе одной и той же пары.
    """
    data = read_data()
    lessons = []
    for day_offset in range((end - start).days + 1):
        day = start + dt.timedelta(days=day_offset)
        for item in data["weekly_lessons"]:
            if item["group_id"] == group and item["weekday"] == day.isoweekday():
                lesson = {k: v for k, v in item.items() if k not in {"weekday", "group_id"}}
                lesson.update(id=item["id"] + ":" + day.isoformat(), date=day.isoformat(), anchor_group_id=group)
                lessons.append(lesson)
    return lessons, data.get("subgroups", {}).get(group, [])


def load_teachers():
    """Необязательный справочник преподавателей. Не нужен — верните None."""
    return read_data()["teachers"]


# ─── ОБЩАЯ ЧАСТЬ: HTTP-ответы ядру по контракту v1 ──────────────────────────

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
            if path.path == "/v1/info":
                source, timezone = load_source()
                return self.reply(200, {"version": "1", "source": source, "timezone": timezone, "staff_directory": load_teachers() is not None})
            if path.path == "/v1/catalog":
                departments, groups = load_catalog()
                return self.reply(200, {"complete": True, "fetched_at": now(), "departments": departments, "groups": groups})
            if path.path == "/v1/teachers":
                teachers = load_teachers()
                if teachers is None:
                    return self.error(501, "not_supported")
                return self.reply(200, {"complete": True, "fetched_at": now(), "teachers": teachers})
            if path.path == "/v1/schedule":
                query = parse_qs(path.query, strict_parsing=True)
                if set(query) != {"group", "from", "to"} or any(len(v) != 1 for v in query.values()):
                    return self.error(400, "invalid_request")
                group, first, last = (query[k][0] for k in ("group", "from", "to"))
                start, end = dt.date.fromisoformat(first), dt.date.fromisoformat(last)
                if not 0 <= (end - start).days <= 30:
                    return self.error(400, "invalid_request")
                if group not in {g["id"] for g in load_catalog()[1]}:
                    return self.error(404, "not_found")
                lessons, subgroups = load_lessons(group, start, end)
                return self.reply(200, {"group_id": group, "from": first, "to": last, "complete": True, "status": "published", "fetched_at": now(), "lessons": lessons, "subgroups": subgroups, "workdays": [1, 2, 3, 4, 5]})
            return self.error(404, "not_found")
        except (ValueError, KeyError):
            return self.error(400, "invalid_request")
        except OSError:
            return self.error(503, "unavailable")
        except Exception:
            # Сбой при получении данных вуза: ядро сохранит прежнее расписание.
            return self.error(502, "upstream_error")

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
