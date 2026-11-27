import React from 'react';
import { FileText, Copy, Download, Info, ShieldCheck, ShieldAlert } from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent, CardFooter } from './ui/card';
import { Button } from './ui/button';
import { Badge } from './ui/badge';
import { Tooltip, TooltipTrigger, TooltipContent } from './ui/tooltip';
import { citationState, markdownNote, claimState, hasClaims, claimRows } from '../lib/citations';

const CHIP_ICONS = { supported: ShieldCheck, unsupported: ShieldAlert, unchecked: Info };
const MARKER_CLASS =
  'inline-flex items-center justify-center font-sans text-xs font-semibold text-primary bg-accent hover:bg-accent/70 rounded px-1 mx-0.5 cursor-pointer align-baseline transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring';

// A `[n]` marker (design FE-4 §2). With a card it controls `ref-<n>` and previews the passage in a tooltip (hover and
// keyboard focus, never takes focus); without one it keeps the FE-1 hover hint. `pos` is the positional citation
// index in claims mode; the prose fallback reports the marker alone, as before.
function MarkerButton({ marker, pos, citation, onCitationClick }) {
  const button = (
    <button
      type="button"
      className={MARKER_CLASS}
      onClick={() => (pos === undefined ? onCitationClick(marker) : onCitationClick(marker, pos))}
      aria-controls={citation ? `ref-${marker}` : undefined}
      title={citation ? undefined : `Click to inspect Evidence [${marker}]`}
    >
      [{marker}]
    </button>
  );
  if (!citation) return button;
  const section = citation.section ? ` · § ${citation.section}` : '';
  return (
    <Tooltip>
      <TooltipTrigger asChild>{button}</TooltipTrigger>
      <TooltipContent>{`[${marker}] ${citation.title}${section} · ${citationState(citation).text}`}</TooltipContent>
    </Tooltip>
  );
}

const SUBTITLES = {
  quoted: 'Quoted from the papers — every sentence appears verbatim in a cited passage',
  synthesized: 'Written answer — each claim checked against the passage it cites',
};

// Refusal panel copy (design FE-3 §2, approved by pm-lead). An unknown future reason shows the server's own text.
const REFUSAL_COPY = {
  all_citations_unsupported:
    "A draft answer was written, but none of its statements was supported by the passages it cited, so it isn't shown. The passages that were found are listed as evidence.",
  verification_unavailable:
    "Citation checking is unavailable right now, so the answer couldn't be checked and isn't shown. The passages that were found are listed as evidence. Try again in a moment.",
  insufficient_evidence:
    "The passages found don't answer this question, so no answer is shown. They are listed as evidence. Try rephrasing the question or indexing more papers.",
};

export default function SynthesisView({ answer, traceId, citations, claims, onCitationClick, onCopy, answerKind, refusalReason, writing }) {
  const titleId = React.useId();
  const cits = citations || [];
  const supportedCount = cits.filter((c) => citationState(c).kind === 'supported').length;

  // The panel link moves focus to the evidence (ReferencesStack's heading has tabIndex -1).
  const focusEvidence = (e) => {
    e.preventDefault();
    const heading = document.getElementById('references-heading');
    if (heading) {
      heading.focus();
      heading.scrollIntoView?.({ behavior: 'smooth', block: 'start' });
    }
  };

  // Parse answer text to render clickable [1], [2] citation badges
  const renderFormattedAnswer = (text) => {
    const parts = [];
    const regex = /\[(\d+)\]/g;
    let lastIndex = 0;
    let match;

    while ((match = regex.exec(text)) !== null) {
      if (match.index > lastIndex) {
        parts.push(text.substring(lastIndex, match.index));
      }
      const marker = parseInt(match[1], 10);
      parts.push(
        <MarkerButton
          key={match.index}
          marker={marker}
          citation={cits.find((c) => c.marker === marker)}
          onCitationClick={onCitationClick}
        />
      );
      lastIndex = regex.lastIndex;
    }

    if (lastIndex < text.length) {
      parts.push(text.substring(lastIndex));
    }

    return parts;
  };

  // Claims mode (design FE-4 §2): each statement with its markers and its own verdict chip.
  const renderClaims = () => (
    <ol aria-label="Claims in this answer" className="list-none pl-0 space-y-2">
      {claimRows(claims, cits).map(({ claim, index, entries }) => {
        const state = claimState(claim);
        const ChipIcon = CHIP_ICONS[state.kind];
        return (
          <li key={index} className="pl-0">
            {claim.text}
            {entries.map((e, j) =>
              e.citation ? (
                <MarkerButton key={j} marker={e.marker} pos={e.pos} citation={e.citation} onCitationClick={onCitationClick} />
              ) : (
                <span key={j} className="font-sans text-xs text-muted-foreground mx-0.5">[{e.marker}]</span>
              ),
            )}{' '}
            <Badge variant={state.kind} className="gap-1 font-sans text-[11px] align-middle">
              <ChipIcon className="w-3 h-3" aria-hidden="true" />
              <span>{state.text}</span>
            </Badge>
          </li>
        );
      })}
    </ol>
  );

  const handleCopySummary = () => {
    onCopy(answer, "📋 Clinical summary copied to clipboard!");
  };

  const handleExportMarkdown = () => {
    onCopy(markdownNote(answer, citations), "📥 Markdown clinical research note copied to clipboard!");
  };

  return (
    <Card className="border-border/80 bg-card/95 shadow-xl relative overflow-hidden">
      <CardHeader className="pb-3 border-b border-border/60 flex flex-row items-center justify-between space-y-0">
        <div className="flex items-center gap-2.5">
          <div className="p-1.5 rounded-lg bg-accent text-accent-foreground">
            <FileText className="w-4 h-4" aria-hidden="true" />
          </div>
          <div>
            <CardTitle className="text-base font-title text-foreground">
              Clinical Literature Synthesis
            </CardTitle>
            <p className="text-xs text-muted-foreground">
              {refusalReason ? 'Evidence only — no answer was shown' : (SUBTITLES[answerKind] || 'Grounded answer')}
            </p>
            {writing && (
              <p className="text-xs text-muted-foreground mt-0.5 motion-safe:animate-pulse" role="status">Writing a summary from these passages…</p>
            )}
          </div>
        </div>

        <Badge variant="arxiv" className="font-mono">
          Trace: {traceId ? traceId.substring(0, 12) + '...' : 'live'}
        </Badge>
      </CardHeader>

      <CardContent className="pt-5">
        {refusalReason ? (
          <section role="status" aria-labelledby={titleId} className="rounded-md border border-border bg-muted p-4 font-sans">
            <h3 id={titleId} className="flex items-center gap-2 text-sm font-semibold text-foreground">
              <Info aria-hidden="true" className="h-4 w-4 text-muted-foreground" /> No answer shown
            </h3>
            <p className="mt-1 text-sm text-foreground">{REFUSAL_COPY[refusalReason] ?? answer}</p>
            {cits.length > 0 && (
              <a
                href="#references-heading"
                onClick={focusEvidence}
                className="mt-2 inline-block text-sm text-primary underline underline-offset-2 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                Read the {cits.length} retrieved passage{cits.length === 1 ? '' : 's'}
              </a>
            )}
          </section>
        ) : (
          <div className="prose font-serif sm:prose-lg max-w-none">
            {hasClaims(claims) ? renderClaims() : <p>{renderFormattedAnswer(answer)}</p>}
          </div>
        )}
      </CardContent>

      {!refusalReason && (
        <CardFooter className="pt-2 pb-4 border-t border-border/40 flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={handleCopySummary}
              className="gap-1.5 text-xs text-muted-foreground hover:text-foreground"
            >
              <Copy className="w-3.5 h-3.5" />
              <span>Copy Synthesis</span>
            </Button>

            <Button
              variant="outline"
              size="sm"
              onClick={handleExportMarkdown}
              className="gap-1.5 text-xs text-muted-foreground hover:text-foreground"
            >
              <Download className="w-3.5 h-3.5" />
              <span>Export Note</span>
            </Button>
          </div>

          <span className="text-xs text-muted-foreground">
            {supportedCount} of {cits.length} citations supported
          </span>
        </CardFooter>
      )}
    </Card>
  );
}
