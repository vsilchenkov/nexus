#!/usr/bin/env python3
"""Создание GitLab Release для проекта Nexus (bus/nexus, project id 138).

Используется на шаге «создать релиз» процесса выпуска (`glab` не установлен —
работаем через REST API). Токен берётся из URL origin (`oauth2:<token>@`),
отдельно хранить не нужно. Тег должен уже существовать в GitLab (быть запушен).

Usage:
    python scripts/release/create_release.py <tag> <name> <descfile>

Пример:
    python scripts/release/create_release.py v1.5.0 "v1.5.0" desc_150.md

где <descfile> — markdown-файл с телом релиза (обычно секция CHANGELOG
[X.Y.Z] + ссылка на compare-diff).
"""
import sys, json, re, subprocess, urllib.request, urllib.error

if len(sys.argv) != 4:
    sys.exit("usage: create_release.py <tag> <name> <descfile>")

tag, name, descfile = sys.argv[1], sys.argv[2], sys.argv[3]
remote = subprocess.check_output(["git", "remote", "get-url", "origin"]).decode().strip()
token = re.search(r"oauth2:([^@]+)@", remote).group(1)
desc = open(descfile, encoding="utf-8").read()
payload = json.dumps({"tag_name": tag, "name": name, "description": desc}).encode("utf-8")
req = urllib.request.Request(
    "https://gitlab.ci.vozovoz.ru/api/v4/projects/138/releases",
    data=payload, method="POST",
)
req.add_header("PRIVATE-TOKEN", token)
req.add_header("Content-Type", "application/json")
try:
    r = urllib.request.urlopen(req)
    print("OK", r.status, json.load(r).get("tag_name"))
except urllib.error.HTTPError as e:
    print("ERR", e.code, e.read().decode("utf-8"))
