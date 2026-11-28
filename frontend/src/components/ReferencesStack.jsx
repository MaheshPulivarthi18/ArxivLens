import React, { useState } from 'react';
import { ExternalLink, FileCode, ShieldCheck, ShieldAlert, Info, BookOpen, Download } from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent, CardFooter } from './ui/card';
import { Button } from './ui/button';
import { Badge } from './ui/badge';
import { Tooltip, TooltipTrigger, TooltipContent } from './ui/tooltip';
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from './ui/collapsible';
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from './ui/dropdown-menu';
import {
  citationState, groupByMarker, hasClaims, claimCitations, locateQuote, pdfHref, metaLine, validPage,
} from '../lib/citations';
import { bibEntry, bibFile, bibFilename } from '../lib/bibtex';

const CHIP_ICONS = { supported: ShieldCheck, unsupported: ShieldAlert, unchecked: Info };
const LONG_PASSAGE = 480;
const BOX = 'text-sm font-serif text-foreground bg-muted/50 border-l-2 border-border rounded-r-md p-3 leading-relaxed whitespace-pre-wrap';

// The chip text carries the meaning; the tooltip only explains it (opens on hover and keyboard focus).
function Chip({ check }) {
  const ChipIcon = CHIP_ICONS[check.kind];
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className="shrink-0 rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card"
        >
          <Badge variant={check.kind} className="gap-1 text-[11px]">
            <ChipIcon className="w-3 h-3" aria-hidden="true" />
            <span>{check.text}</span>
          </Badge>
        </button>
      </TooltipTrigger>
      <TooltipContent>{check.tooltip}</TooltipContent>
    </Tooltip>
  );
}

// `text` is content.slice(from); ranges are sorted [start, end) raw offsets (overlaps are clipped).
function Marked({ text, ranges, from = 0 }) {
  const out = [];
  let i = 0;
  for (const [s, e] of ranges) {
    const a = Math.max(s - from, i);
    const b = Math.min(e - from, text.length);
    if (b <= a) continue;
    if (a > i) out.push(text.slice(i, a));
    out.push(
      <mark key={out.length} className="bg-accent text-accent-foreground underline decoration-2 underline-offset-2 rounded-sm">
        <span className="sr-only">Quote: </span>
        {text.slice(a, b)}
      </mark>,
    );
    i = b;
  }
  if (i < text.length) out.push(text.slice(i));
  return out;
}

// Collapsed view of a long passage: ±200 chars around the first highlight, or the first 320, cut at spaces.
function excerptBounds(content, ranges) {
  const [qs, qe] = ranges[0] || [0, 0];
  let s = ranges[0] ? Math.max(0, qs - 200) : 0;
  let e = ranges[0] ? Math.min(content.length, qe + 200) : 320;
  const sp = content.indexOf(' ', s);
  if (s > 0 && sp >= 0 && sp < qs) s = sp + 1;
  const ep = content.lastIndexOf(' ', e);
  if (e < content.length && ep > qe) e = ep;
  return [s, e];
}

function Passage({ content, ranges }) {
  const [open, setOpen] = useState(false);
  if (content.length <= LONG_PASSAGE) {
    return <div className={BOX}><Marked text={content} ranges={ranges} /></div>;
  }
  const [s, e] = excerptBounds(content, ranges);
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      {!open && (
        <div className={BOX} data-excerpt="true">
          {s > 0 && '…'}
          <Marked text={content.slice(s, e)} ranges={ranges} from={s} />
          {e < content.length && '…'}
        </div>
      )}
      <CollapsibleContent className={BOX}>
        <Marked text={content} ranges={ranges} />
      </CollapsibleContent>
      <CollapsibleTrigger asChild>
        <Button variant="ghost" size="sm" className="h-7 mt-1 text-xs text-muted-foreground hover:text-foreground">
          {open ? 'Show less' : 'Show full passage'}
        </Button>
      </CollapsibleTrigger>
    </Collapsible>
  );
}

// Highlight only for supported checks: a quote on any other status would read as evidence for a failed check.
function quoteState(content, entries, refusal) {
  const supported = entries.filter(({ citation }) => citationState(citation, { refusal }).kind === 'supported');
  const ranges = [];
  const missing = [];
  for (const { citation } of supported) {
    const q = typeof citation.quote === 'string' ? citation.quote.trim() : '';
    if (!q) continue;
    const r = locateQuote(content, citation.quote);
    if (r) ranges.push([r.start, r.end]);
    else if (!missing.includes(q)) missing.push(q);
  }
  ranges.sort((x, y) => x[0] - y[0]);
  // The no-quote note needs an explicit `status` (a response from before quotes existed has none, AC6).
  const checked = supported.some(({ citation }) => citation.status === 'supported');
  return { ranges, missing, noQuote: checked && !ranges.length && !missing.length };
}

function downloadBib(citations, answerKind, onNotify) {
  const { text, count } = bibFile(citations, { answerKind });
  const url = URL.createObjectURL(new Blob([text], { type: 'application/x-bibtex' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = bibFilename();
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  onNotify?.(`Exported ${count} ${count === 1 ? 'reference' : 'references'} (.bib)`);
}

// `active` = { marker, pos } | null: the card the reader came to from a marker (design FE-4 §4). Ignored in refusal
// mode, which has no markers. Cards are grouped by marker (one per passage) and are focus targets, not Tab stops.
export default function ReferencesStack({ citations, claims, answerKind, active, traceId, onCopy, onNotify, refusalReason }) {
  if (!citations || citations.length === 0) {
    return (
      <Card className="border-border/60 bg-card/60 p-6 text-center text-muted-foreground text-sm">
        <BookOpen className="w-8 h-8 mx-auto mb-2 opacity-40" />
        <p>No reference papers cited for this query.</p>
      </Card>
    );
  }

  const refusal = !!refusalReason;
  const groups = groupByMarker(citations);
  // pos → claim number, only when the positional invariant holds; otherwise rows read "Check n".
  const claimOf = new Map();
  for (const row of (hasClaims(claims) && claimCitations(claims, citations)) || []) {
    for (const { pos } of row.entries) claimOf.set(pos, row.index + 1);
  }
  const exportable = !refusal && bibFile(citations, { answerKind }).count > 0;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between px-1">
        <h3
          id="references-heading"
          tabIndex={-1}
          className="text-sm font-semibold text-foreground flex items-center gap-2 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <span>{refusalReason ? 'Retrieved passages (no answer was shown)' : 'References'}</span>
          <Badge variant="secondary" className="text-xs px-2 py-0">
            {groups.length} {refusalReason ? (groups.length === 1 ? 'passage' : 'passages') : 'cited'}
          </Badge>
        </h3>
        {!refusalReason && (
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">Click [n] in text to inspect</span>
            {/* Refusal mode: no Export at all (a disabled trigger is skipped by Tab, so its reason would be unreachable). */}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" className="h-7 text-xs gap-1.5">
                  <Download className="w-3 h-3" aria-hidden="true" />
                  <span>Export</span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem disabled={!exportable} onSelect={() => downloadBib(citations, answerKind, onNotify)}>
                  BibTeX (.bib)
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        )}
      </div>

      {groups.map(({ marker, entries }) => {
        const cit = entries[0].citation;
        const isHighlighted = !refusalReason && active?.marker === marker;
        const content = typeof cit.content === 'string' ? cit.content : '';
        const { ranges, missing, noQuote } = quoteState(content, entries, refusal);
        const page = validPage(cit.page) ? cit.page : null;
        const bib = bibEntry(cit);

        return (
          <Card
            key={`${traceId}-${marker}`}
            id={`ref-${marker}`}
            role="article"
            aria-label={`Citation [${cit.marker}]: ${cit.title}`}
            tabIndex={-1}
            data-active={isHighlighted ? 'true' : undefined}
            className={`transition-all duration-300 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
              isHighlighted
                ? 'border-primary ring-2 ring-ring/30 bg-card shadow-lg'
                : 'border-border/70 hover:border-border bg-card/90'
            }`}
          >
            <CardHeader className="p-4 pb-2">
              <div className="flex items-start justify-between gap-3">
                <div className="flex items-center gap-2">
                  <span className="flex items-center justify-center w-6 h-6 rounded-md bg-accent text-accent-foreground font-mono font-bold text-xs">
                    [{cit.marker}]
                  </span>
                </div>
                {entries.length === 1 && <Chip check={citationState(cit, { refusal })} />}
              </div>

              <CardTitle className="text-sm font-semibold text-foreground mt-2 leading-snug">
                {cit.title}
              </CardTitle>
              <p className="text-xs text-muted-foreground mt-0.5">{metaLine(cit)}</p>

              {/* A passage cited by several claims: one row per check (design FE-4 §3). */}
              {entries.length > 1 && (
                <ul aria-label="Checks of this passage" className="mt-2 space-y-1">
                  {entries.map(({ citation, pos }, k) => {
                    const current = isHighlighted && active?.pos === pos;
                    return (
                      <li
                        key={pos}
                        aria-current={current ? 'true' : undefined}
                        className={`flex items-center justify-between gap-2 pl-2 border-l-2 text-xs text-muted-foreground ${
                          current ? 'border-primary text-foreground' : 'border-transparent'
                        }`}
                      >
                        <span>{claimOf.has(pos) ? `Cited by claim ${claimOf.get(pos)}` : `Check ${k + 1}`}</span>
                        <Chip check={citationState(citation, { refusal })} />
                      </li>
                    );
                  })}
                </ul>
              )}
            </CardHeader>

            <CardContent className="p-4 pt-1">
              {content ? (
                <Passage content={content} ranges={ranges} />
              ) : (
                <div className={BOX}>Passage text isn&apos;t available. Open the PDF to read it.</div>
              )}
              {content &&
                missing.map((q) => (
                  <blockquote key={q} className="mt-2 pl-3 border-l-2 border-border text-xs text-muted-foreground">
                    Quoted text (not found in the passage shown above): “{q}”
                  </blockquote>
                ))}
              {content && noQuote && (
                <p className="mt-2 text-xs text-muted-foreground">
                  No exact quote for this citation: the check compared the claim with the whole passage.
                </p>
              )}
            </CardContent>

            <CardFooter className="p-4 pt-0 border-t border-border/40 flex items-center justify-between gap-2 mt-2">
              <Button
                asChild
                variant="outline"
                size="sm"
                className="text-xs text-primary border-input hover:bg-accent hover:text-accent-foreground gap-1.5 h-7"
              >
                <a
                  href={pdfHref(cit)}
                  target="_blank"
                  rel="noreferrer"
                  aria-label={`Open PDF for [${cit.marker}]${page ? ` at page ${page}` : ''} (opens in a new tab)`}
                >
                  <ExternalLink className="w-3 h-3" aria-hidden="true" />
                  <span>{page ? `Open PDF at p. ${page} ↗` : 'Open PDF ↗'}</span>
                </a>
              </Button>

              {bib && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onCopy(bib, `📋 BibTeX reference copied for [${cit.marker}]!`)}
                  aria-label={`Copy BibTeX for [${cit.marker}]`}
                  className="text-xs text-muted-foreground hover:text-foreground gap-1.5 h-7"
                >
                  <FileCode className="w-3 h-3" />
                  <span>Copy BibTeX</span>
                </Button>
              )}
            </CardFooter>
          </Card>
        );
      })}
    </div>
  );
}
