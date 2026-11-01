"""Resolve every relative markdown link and every backticked repo path in the changed PRD sections.
For each target: exists at HEAD (PM-1)?  exists at origin/agent/ai/EVAL-1 (PR #2)?"""
import re, subprocess, os, sys
def exists(ref, path):
    return subprocess.run(["git","cat-file","-e",f"{ref}:{path}"],capture_output=True).returncode==0
def show(ref, path):
    return subprocess.run(["git","show",f"{ref}:{path}"],capture_output=True,text=True).stdout
refs = {"HEAD":"HEAD","EVAL-1(PR#2)":"origin/agent/ai/EVAL-1"}
targets = {}
for doc in ["docs/PRD-Product.md","docs/PRD-Technical.md"]:
    text = show("HEAD", doc)
    base = os.path.dirname(doc)
    for m in re.finditer(r"\]\(([^)#\s]+)\)", text):
        t = m.group(1)
        if t.startswith("http"): continue
        targets.setdefault(os.path.normpath(os.path.join(base,t)), set()).add(doc+" (link)")
    for m in re.finditer(r"`((?:evals|scripts|tests|agents|migrations|frontend)/[A-Za-z0-9_./-]+?)(?:::(\w+))?`", text):
        p = m.group(1).rstrip("/.")
        targets.setdefault(p, set()).add(doc+" (path)")
        if m.group(2):
            targets.setdefault(p+"::"+m.group(2), set()).add(doc+" (test fn)")
dangling_after_merge = []
print(f"{'target':58} {'HEAD':5} {'+PR#2':5}  cited in")
for t in sorted(targets):
    if "::" in t:
        f, fn = t.split("::")
        r = {k: (f"def {fn}" in show(v, f)) for k,v in refs.items()}
    else:
        r = {k: exists(v, t) for k,v in refs.items()}
    a, b = r["HEAD"], r["EVAL-1(PR#2)"]
    mark = "ok" if a else ("PR#2" if b else "MISSING")
    print(f"{t:58} {'yes' if a else 'NO':5} {'yes' if b else 'NO':5}  {', '.join(sorted(targets[t]))}  [{mark}]")
    if not a: dangling_after_merge.append((t, b))
print()
print("Targets absent at PM-1 HEAD (dangle if #3 merges before #2):")
for t,b in dangling_after_merge:
    print(f"  {t}   -> {'added by PR #2 (EVAL-1)' if b else 'NOT in PR #2 either'}")
