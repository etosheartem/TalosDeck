#!/usr/bin/env python3
"""Вклеивает отчёты агентов (.bugfix-reports/NN.md) под каждый баг в BUGS.md."""
import re, glob, pathlib

root = pathlib.Path(__file__).resolve().parent
bugs = root.parent / "BUGS.md"

reports = {}
for f in sorted(glob.glob(str(root / "[0-9][0-9].md"))):
    for m in re.finditer(r"<<<BUG\s+([A-Z0-9-]+)\s*>>>\n(.*?)\n?<<<END>>>", pathlib.Path(f).read_text(), re.S):
        reports[m.group(1).strip()] = m.group(2).strip()

src = (root / "BUGS.md.orig").read_text()
# режем на блоки по заголовкам '### '
parts = re.split(r"(?m)^(?=### )", src)
out, used = [], set()
for p in parts:
    m = re.match(r"### \[[A-Z]+\] ([A-Z0-9-]+):", p)
    if m and m.group(1) in reports:
        body = p.rstrip("\n")
        tail = ""
        # хвост секции ('---' и следующий '## ') держим после отчёта
        tm = re.search(r"(?m)^(---\s*$)", body)
        if tm:
            tail = body[tm.start():]
            body = body[:tm.start()].rstrip("\n")
        out.append(body + "\n\n> **ОТЧЁТ ОБ ИСПРАВЛЕНИИ**\n>\n"
                   + "\n".join("> " + l if l.strip() else ">" for l in reports[m.group(1)].splitlines())
                   + "\n\n" + tail + ("\n" if tail else "\n"))
        used.add(m.group(1))
    else:
        out.append(p)

bugs.write_text("".join(out))
miss = sorted(set(reports) - used)
print(f"вклеено: {len(used)}  осталось без заголовка в BUGS.md: {miss or '—'}")
ids = {re.match(r"### \[[A-Z]+\] ([A-Z0-9-]+):", p).group(1) for p in parts if re.match(r"### \[[A-Z]+\] ([A-Z0-9-]+):", p)}
print("без отчёта:", sorted(ids - used) or "—")
