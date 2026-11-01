"""AC1: every ⏳/◐ row in Product PRD §6 has an acceptance statement or a linked task that exists."""
import re, subprocess, os
def show(p): return subprocess.run(["git","show",f"HEAD:{p}"],capture_output=True,text=True).stdout
def exists(p): return subprocess.run(["git","cat-file","-e",f"HEAD:{p}"],capture_output=True).returncode==0
txt = show("docs/PRD-Product.md")
sec = re.split(r"\n## 7\.", re.split(r"\n## 6\.", txt)[1])[0]
rows = [l for l in sec.splitlines() if re.match(r"\| (ING|QA|UI|OPS)-\d+ ", l)]
print(f"Product PRD §6: {len(rows)} requirement rows")
bad = 0
for l in rows:
    c = [x.strip() for x in l.strip("|").split("|")]
    rid, status, acc = c[0], c[3], c[4] if len(c) > 4 else ""
    if "✅" in status and "◐" not in status:
        continue
    links = re.findall(r"\]\(\.\./(agents/tasks/[^)]+)\)", acc)
    linked = [f"{p} {'exists' if exists(p) else 'MISSING'}" for p in links]
    has_stmt = "acceptance" in acc.lower() or ":" in acc
    ok = (links and all(exists(p) for p in links)) or has_stmt
    bad += not ok
    print(f"\n{rid} [{status} = {'+'.join(__import__('unicodedata').name(ch) for ch in status if ord(ch)>127)}] -> {'OK' if ok else 'NO ACCEPTANCE'}")
    print("   links: " + (", ".join(linked) or "none"))
    print("   text : " + (acc[:230] + ("..." if len(acc) > 230 else "")))
print(f"\nRows not ✅ without acceptance/task: {bad}")
t = show("docs/PRD-Technical.md").split("## 11. Roadmap")[1].split("## 12.")[0]
print("\nTechnical PRD §11 roadmap 'Task' column:")
for l in t.splitlines():
    if re.match(r"\| P\d ", l):
        c = [x.strip() for x in l.strip("|").split("|")]
        task = c[3]
        links = re.findall(r"\]\(\.\./(agents/tasks/[^)]+)\)", task)
        st = ", ".join(f"{p} {'exists' if exists(p) else 'MISSING'}" for p in links) or task
        print(f"  {c[0]:3} {c[1][:44]:46} -> {st}")
