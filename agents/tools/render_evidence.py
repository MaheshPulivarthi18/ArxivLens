#!/usr/bin/env python3
"""Render real command output into a labelled PNG "screenshot" for QA evidence.

The image shows exactly what the command printed, with a header saying what was run, on which commit, and when,
so a reviewer can trust it. It never edits or summarises the output: long output is cut with a visible
"... N more lines" marker, and the full text is saved next to the image.

Usage:
  python3 agents/tools/render_evidence.py --title "Unit tests" --out agents/reviews/EVAL-1/evidence/01-unit-tests.png \
      -- pytest -q tests/test_evals_data.py
  python3 agents/tools/render_evidence.py --title "CI checks" --out .../02-ci.png --from-file ci.txt --command "gh api .../check-runs"
"""
import argparse, datetime, os, subprocess, sys
from PIL import Image, ImageDraw, ImageFont

FONT_PATHS = ["/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf", "/System/Library/Fonts/Menlo.ttc"]
MAX_LINES, MAX_COLS = 60, 140
BG, FG, DIM, HEAD_BG, OK, BAD = (24, 26, 31), (220, 223, 228), (140, 146, 156), (40, 44, 52), (120, 200, 120), (235, 110, 110)


def font(size):
    for p in FONT_PATHS:
        if os.path.exists(p):
            return ImageFont.truetype(p, size)
    return ImageFont.load_default()


def git(*args):
    try:
        return subprocess.run(["git", *args], capture_output=True, text=True, check=True).stdout.strip()
    except Exception:
        return "unknown"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--title", required=True)
    ap.add_argument("--out", required=True, help="PNG path; full text is saved next to it as .txt")
    ap.add_argument("--from-file", help="render this file instead of running a command")
    ap.add_argument("--command", help="command label to show when using --from-file")
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    a = ap.parse_args()

    if a.from_file:
        text, rc, label = open(a.from_file, encoding="utf-8", errors="replace").read(), None, a.command or a.from_file
    else:
        cmd = a.cmd[1:] if a.cmd[:1] == ["--"] else a.cmd
        if not cmd:
            sys.exit("give a command after -- or use --from-file")
        p = subprocess.run(cmd, capture_output=True, text=True)
        text, rc, label = p.stdout + p.stderr, p.returncode, " ".join(cmd)

    os.makedirs(os.path.dirname(os.path.abspath(a.out)), exist_ok=True)
    with open(os.path.splitext(a.out)[0] + ".txt", "w", encoding="utf-8") as f:
        f.write(f"$ {label}\n{text}\n" + ("" if rc is None else f"[exit code {rc}]\n"))

    lines = text.rstrip("\n").splitlines() or ["(no output)"]
    shown = [l[:MAX_COLS] + ("…" if len(l) > MAX_COLS else "") for l in lines[:MAX_LINES]]
    if len(lines) > MAX_LINES:
        shown.append(f"... {len(lines) - MAX_LINES} more lines (full output in {os.path.basename(os.path.splitext(a.out)[0])}.txt)")

    f_body, f_head = font(14), font(15)
    lh = 19
    header = [
        a.title,
        f"$ {label[:MAX_COLS]}",
        f"commit {git('rev-parse', '--short', 'HEAD')} on {git('rev-parse', '--abbrev-ref', 'HEAD')}  ·  "
        f"{datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%d %H:%M UTC')}"
        + ("" if rc is None else f"  ·  exit code {rc}"),
    ]
    width = 24 + 8.6 * max(len(s) for s in shown + header) + 24
    height = 16 + lh * len(header) + 14 + lh * len(shown) + 16
    img = Image.new("RGB", (int(max(width, 640)), int(height)), BG)
    d = ImageDraw.Draw(img)
    d.rectangle([0, 0, img.width, 16 + lh * len(header) + 4], fill=HEAD_BG)
    y = 12
    for i, h in enumerate(header):
        color = FG if i == 0 else DIM
        if i == 2 and rc is not None:
            color = OK if rc == 0 else BAD
        d.text((24, y), h, font=f_head if i == 0 else f_body, fill=color)
        y += lh
    y += 18
    for l in shown:
        low = l.lower()
        color = BAD if any(k in low for k in ("fail", "error", "traceback")) else OK if any(k in low for k in (" passed", "success", " ok")) else FG
        d.text((24, y), l, font=f_body, fill=color)
        y += lh
    img.save(a.out)
    print(a.out)
    sys.exit(0 if rc in (None, 0) else rc)


if __name__ == "__main__":
    main()
