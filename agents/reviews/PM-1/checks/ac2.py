"""AC2: every open question links an ADR that exists; every linked/new ADR is Status: Proposed."""
import re, subprocess
def show(ref, p): return subprocess.run(["git","show",f"{ref}:{p}"],capture_output=True,text=True).stdout
def section(doc, start, end):
    t = show("HEAD", doc); return re.split(end, re.split(start, t)[1])[0]
for doc, start, end, label in [("docs/PRD-Product.md", r"\n## 11\.", r"\n## 12\.|\Z", "Product PRD §11"),
                               ("docs/PRD-Technical.md", r"\n## 12\.", r"\n## 13\.|\Z", "Technical PRD §12")]:
    print(f"== {label} (HEAD)")
    for l in section(doc, start, end).splitlines():
        m = re.match(r"(\d+)\. (.*)", l)
        if not m: continue
        q = m.group(2)
        links = re.findall(r"\]\((adr/[^)]+)\)", q)
        if not links:
            print(f"  Q{m.group(1)}: NO ADR LINK  | {q[:90]}"); continue
        for a in links:
            body = show("HEAD", "docs/"+a)
            title = body.splitlines()[0] if body else "MISSING FILE"
            st = re.search(r"\*\*Status:\*\*\s*(.+)", body)
            print(f"  Q{m.group(1)} -> docs/{a}\n        title : {title}\n        status: {st.group(1) if st else '??'}")
    print("  main had:", len([l for l in section(doc, start, end).splitlines() if re.match(r'\d+\. ', l)]), "questions on HEAD;",
          len([l for l in re.split(end, re.split(start, show('origin/main', doc))[1])[0].splitlines() if re.match(r'\d+\. ', l)]), "on origin/main")
print("\n== ADRs added by this PR (git diff origin/main...HEAD --diff-filter=A docs/adr)")
new = subprocess.run(["git","diff","--name-only","--diff-filter=A","origin/main...HEAD","--","docs/adr"],capture_output=True,text=True).stdout.split()
for f in new:
    b = show("HEAD", f); st = re.search(r"\*\*Status:\*\*\s*(.+)", b)
    dec = re.split(r"\n## Decision\n", b)[1].split("\n## ")[0].strip()
    print(f"  {f}: Status={st.group(1)} | Decision section: {dec}")
print("\nAny ADR on HEAD with Status Accepted?", subprocess.run(["git","grep","-n","Status:\\*\\* *Accepted","HEAD","--","docs/adr"],capture_output=True,text=True).stdout.strip() or "none")
